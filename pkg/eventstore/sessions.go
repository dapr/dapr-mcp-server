package eventstore

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"
)

// sessionCloseTimeout bounds the state store calls of SessionClosed,
// which the SDK makes with a context that has no deadline.
const sessionCloseTimeout = 30 * time.Second

// sessionState caches how many streams a session has opened, and serializes
// changes to the session's records.
type sessionState struct {
	mu     sync.Mutex
	loaded bool
	opened int

	// cached holds the session's entries in Store.streams. Guarded by Store.mu.
	cached map[string]*streamState
}

// loadSession reads the session record and returns the number of streams it lists,
// keeping the larger of that and the cached number. The caller holds ss.mu.
func (s *Store) loadSession(ctx context.Context, ss *sessionState, sessionID string) (int, error) {
	if ss.loaded {
		return ss.opened, nil
	}
	var rec sessionRecord
	if _, err := s.get(ctx, sessionKey(s.prefix, sessionID), &rec); err != nil {
		return 0, err
	}
	ss.opened = max(ss.opened, rec.Streams)
	ss.loaded = true
	return ss.opened, nil
}

// SessionClosed implements mcp.EventStore by deleting every record of the session.
// From the start of the call, Open and Append refuse the session with ErrSessionClosed,
// and writes already under way finish before the records are listed.
// The in-memory state of the session is dropped even when the delete fails;
// the TTL then reclaims the records.
func (s *Store) SessionClosed(ctx context.Context, sessionID string) error {
	ctx, cancel := context.WithTimeout(ctx, sessionCloseTimeout)
	defer cancel()
	ss, streams := s.markClosed(sessionID)
	defer s.dropSession(sessionID)
	counts := settledCounts(streams)

	ss.mu.Lock()
	defer ss.mu.Unlock()
	// Read the record even when cached, in case another process added streams.
	ss.loaded = false
	keys, err := s.sessionKeys(ctx, ss, sessionID, counts)
	if err != nil {
		return err
	}
	if err := s.deleteBulk(ctx, keys); err != nil {
		return fmt.Errorf("event store: delete session %q from state store %q: %w", sessionID, s.storeName, err)
	}
	return nil
}

// markClosed records that sessionID is closed and returns its state and cached streams.
// Once it returns, acquire refuses the session, so the streams returned are all it has here.
func (s *Store) markClosed(sessionID string) (*sessionState, []*streamState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed.add(sessionID, s.now())
	ss := s.sessionLocked(sessionID)
	return ss, slices.Collect(maps.Values(ss.cached))
}

// settledCounts waits for each stream's write in progress, if any, and returns the count
// of each stream this process has read or written. The caller holds no stream lock.
func settledCounts(streams []*streamState) map[string]int {
	counts := make(map[string]int, len(streams))
	for _, st := range streams {
		st.mu.Lock()
		if st.loaded {
			counts[st.ref.stream] = st.count
		}
		st.mu.Unlock()
	}
	return counts
}

// checkOpen refuses a session that SessionClosed has closed. Open and Append call it while
// holding the stream's lock, which SessionClosed waits for before it lists the records.
func (s *Store) checkOpen(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.checkOpenLocked(sessionID)
}

func (s *Store) checkOpenLocked(sessionID string) error {
	if s.closed.contains(sessionID, s.now()) {
		return sessionClosed(sessionID)
	}
	return nil
}

// sessionKeys lists every key the session wrote: its record, its stream index, and each
// stream's record and events. The streams are those in the index and those in cached,
// the counts this process holds, whose index entry may have expired while the stream
// lived on. The caller holds ss.mu.
func (s *Store) sessionKeys(ctx context.Context, ss *sessionState, sessionID string, cached map[string]int) ([]string, error) {
	n, err := s.loadSession(ctx, ss, sessionID)
	if err != nil {
		return nil, err
	}
	keys := []string{sessionKey(s.prefix, sessionID)}
	indexKeys := make([]string, n)
	for i := range n {
		indexKeys[i] = streamIndexKey(s.prefix, sessionID, i)
	}
	keys = append(keys, indexKeys...)
	indexed, err := s.indexedStreams(ctx, indexKeys)
	if err != nil {
		return nil, err
	}
	streamKeys, err := s.streamKeys(ctx, sessionID, withCachedStreams(indexed, cached), cached)
	if err != nil {
		return nil, err
	}
	return append(keys, streamKeys...), nil
}

// streamKeys lists the record and event keys of streamIDs. A stream's event count is the
// larger of its stored count and its count in cached, which is ahead when a count write
// failed. A stream with no record and no cached count has expired; its events are older
// and are left to expire too.
//
// It lists every event index each stream ever had, including ones long expired, so the cost of
// closing grows with the session's life; keeping a low-water mark of expired indices per stream
// would bound it.
func (s *Store) streamKeys(ctx context.Context, sessionID string, streamIDs []string, cached map[string]int) ([]string, error) {
	recordKeys := make([]string, len(streamIDs))
	for i, streamID := range streamIDs {
		recordKeys[i] = streamKey(s.prefix, sessionID, streamID)
	}
	records, err := s.getBulk(ctx, recordKeys)
	if err != nil {
		return nil, err
	}
	var keys []string
	for i, raw := range records {
		var rec streamRecord
		found, err := decode(raw, &rec)
		if err != nil {
			return nil, fmt.Errorf("event store: %q in state store %q: %w", recordKeys[i], s.storeName, err)
		}
		count, isCached := cached[streamIDs[i]]
		if !found && !isCached {
			continue
		}
		keys = append(keys, recordKeys[i])
		for e := range max(rec.Count, count) {
			keys = append(keys, eventKey(s.prefix, sessionID, streamIDs[i], e))
		}
	}
	return keys, nil
}

// indexedStreams reads the stream IDs under indexKeys, skipping entries that have expired.
func (s *Store) indexedStreams(ctx context.Context, indexKeys []string) ([]string, error) {
	values, err := s.getBulk(ctx, indexKeys)
	if err != nil {
		return nil, err
	}
	streamIDs := make([]string, 0, len(values))
	for i, raw := range values {
		var rec streamIndexRecord
		found, err := decode(raw, &rec)
		if err != nil {
			return nil, fmt.Errorf("event store: %q in state store %q: %w", indexKeys[i], s.storeName, err)
		}
		if found {
			streamIDs = append(streamIDs, rec.Stream)
		}
	}
	return streamIDs, nil
}

// withCachedStreams returns streamIDs plus the streams in cached that it lacks.
func withCachedStreams(streamIDs []string, cached map[string]int) []string {
	seen := make(map[string]bool, len(streamIDs))
	for _, id := range streamIDs {
		seen[id] = true
	}
	all := slices.Clone(streamIDs)
	for _, id := range slices.Sorted(maps.Keys(cached)) {
		if !seen[id] {
			all = append(all, id)
		}
	}
	return all
}

// session returns the in-memory state for a session, creating it on first use.
func (s *Store) session(sessionID string) *sessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionLocked(sessionID)
}

func (s *Store) sessionLocked(sessionID string) *sessionState {
	ss, ok := s.sessions[sessionID]
	if !ok {
		ss = &sessionState{cached: make(map[string]*streamState)}
		s.sessions[sessionID] = ss
	}
	return ss
}

// dropSession forgets the in-memory state of a session and its cached streams.
func (s *Store) dropSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ss, ok := s.sessions[sessionID]; ok {
		for streamID := range ss.cached {
			delete(s.streams, streamRef{session: sessionID, stream: streamID})
		}
	}
	delete(s.sessions, sessionID)
}
