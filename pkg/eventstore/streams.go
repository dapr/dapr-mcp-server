package eventstore

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// streamIdleTimeout is how long an unused stream's count stays cached. The SDK opens a
	// stream per request and never says when one is finished, so without eviction the cache
	// would grow with every request of a long-lived session.
	streamIdleTimeout   = 5 * time.Minute
	streamSweepInterval = time.Minute
	// standaloneStream is the stream a client's GET opens for messages outside any request.
	// It can stay silent for longer than any record TTL, so its count is never evicted.
	standaloneStream = ""
)

// streamRef names a stream within a session.
type streamRef struct {
	session string
	stream  string
}

// streamState caches one stream's count.
type streamState struct {
	ref streamRef

	mu     sync.Mutex // guards the fields below; held across state store calls for this stream
	loaded bool
	// count is the number of indices used, including ones whose event failed to save.
	count int
	// unreserved counts Appends that failed before the count was known.
	// Each used an index the SDK will not reuse.
	unreserved int
	// unsaved reports that count is ahead of the stream record.
	unsaved bool

	// pinned keeps the entry cached while it holds what the stream record does not.
	pinned atomic.Bool

	// Guarded by Store.mu.
	refs     int
	lastUsed time.Time
}

// updatePinned records whether evicting st would lose information. The caller holds st.mu.
func (st *streamState) updatePinned() {
	st.pinned.Store(st.unreserved > 0 || st.unsaved)
}

// acquire returns the cached state for a stream, creating it if needed, and keeps it
// cached until release. It refuses a session that SessionClosed has closed.
func (s *Store) acquire(ref streamRef) (*streamState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkOpenLocked(ref.session); err != nil {
		return nil, err
	}
	s.sweepLocked()
	st, ok := s.streams[ref]
	if !ok {
		st = &streamState{ref: ref}
		s.streams[ref] = st
		s.sessionLocked(ref.session).cached[ref.stream] = st
	}
	st.refs++
	return st, nil
}

// acquireCached is acquire for a stream that is already cached; it never adds an entry.
func (s *Store) acquireCached(ref streamRef) (*streamState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.streams[ref]
	if ok {
		st.refs++
	}
	return st, ok
}

// release ends a use that acquire started. The caller no longer holds st.mu.
func (s *Store) release(st *streamState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st.refs--
	st.lastUsed = s.now()
}

// sweepLocked evicts streams that are idle, unused and fully persisted, except standalone
// streams. The caller holds s.mu.
func (s *Store) sweepLocked() {
	now := s.now()
	if now.Sub(s.lastSweep) < streamSweepInterval {
		return
	}
	s.lastSweep = now
	for ref, st := range s.streams {
		if ref.stream != standaloneStream && st.refs == 0 && !st.pinned.Load() && now.Sub(st.lastUsed) >= streamIdleTimeout {
			delete(s.streams, ref)
			if ss, ok := s.sessions[ref.session]; ok {
				delete(ss.cached, ref.stream)
			}
		}
	}
}

// load fills st from the stream record unless it is already loaded, and reports whether
// the stream exists. The caller holds st.mu.
func (s *Store) load(ctx context.Context, st *streamState, ref streamRef) (bool, error) {
	if st.loaded {
		return true, nil
	}
	var rec streamRecord
	found, err := s.get(ctx, streamKey(s.prefix, ref.session, ref.stream), &rec)
	if err != nil || !found {
		return false, err
	}
	st.count = rec.Count + st.unreserved
	st.unreserved = 0
	st.unsaved = st.count != rec.Count
	st.loaded = true
	return true, nil
}

// streamCount returns the number of indices a stream has used. It reads the stream record
// when this process holds no state for the stream, without caching it: the stream ID comes
// from the client's Last-Event-ID, so caching it would let a client grow memory at will.
func (s *Store) streamCount(ctx context.Context, ref streamRef) (int, bool, error) {
	if st, ok := s.acquireCached(ref); ok {
		defer s.release(st)
		st.mu.Lock()
		defer st.mu.Unlock()
		defer st.updatePinned()
		found, err := s.load(ctx, st, ref)
		return st.count, found, err
	}
	var rec streamRecord
	found, err := s.get(ctx, streamKey(s.prefix, ref.session, ref.stream), &rec)
	return rec.Count, found, err
}
