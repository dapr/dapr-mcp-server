// Package eventstore implements the MCP SDK's EventStore on top of a Dapr state store,
// so a client whose streamable HTTP stream drops can resume it with Last-Event-ID.
package eventstore

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strconv"
	"sync"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// DefaultKeyPrefix namespaces the keys this store writes.
	DefaultKeyPrefix = "dapr-mcp-server-events"
	// DefaultTTL bounds how long events are kept. The SDK does not promise to call
	// SessionClosed, so expiry is what reclaims storage for abandoned sessions.
	DefaultTTL = time.Hour
	// recordTTLFactor is how many times longer than events the fixed-size bookkeeping
	// records (session, stream index and stream count) live. A stream keeps its numbering
	// only while its count is known, and a stream can stay silent for longer than the
	// event TTL: a slow tool call, or the standalone stream of a busy session.
	recordTTLFactor = 24
	// NoTTL disables expiry, for state stores that do not support ttlInSeconds.
	NoTTL = time.Duration(-1)

	ttlMetadataKey  = "ttlInSeconds"
	bulkParallelism = 10
	// maxBulkKeys caps the keys of one bulk call, which must fit Dapr's default 4MB gRPC message.
	maxBulkKeys = 500
)

// StateClient is the part of the Dapr client the store uses.
type StateClient interface {
	GetState(ctx context.Context, storeName, key string, meta map[string]string) (*dapr.StateItem, error)
	SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...dapr.StateOption) error
	GetBulkState(ctx context.Context, storeName string, keys []string, meta map[string]string, parallelism int32) ([]*dapr.BulkStateItem, error)
	DeleteBulkState(ctx context.Context, storeName string, keys []string, meta map[string]string) error
}

// Options configures a Store.
type Options struct {
	// StoreName is the Dapr state store component that holds the events. Required.
	StoreName string
	// KeyPrefix namespaces the keys. Empty means DefaultKeyPrefix.
	KeyPrefix string
	// TTL is how long each event lives, a whole number of seconds because the state store's
	// ttlInSeconds takes seconds. Zero means DefaultTTL; NoTTL disables expiry.
	TTL time.Duration
}

// Store is an mcp.EventStore backed by the Dapr state API.
//
// The event count of each recently used stream is cached in memory and writes to one
// stream are serialized, so a stream must be written by one process at a time. That holds
// for the stateful streamable HTTP transport, where a session lives in the process that
// created it.
type Store struct {
	client    StateClient
	storeName string
	prefix    string
	// eventMeta and recordMeta are the save metadata of events and of bookkeeping records.
	eventMeta  map[string]string
	recordMeta map[string]string
	now        func() time.Time

	// mu guards the fields below. It is never held across a state store call,
	// so one slow call cannot stall other sessions.
	mu        sync.Mutex
	streams   map[streamRef]*streamState
	sessions  map[string]*sessionState
	closed    *closedSessions
	lastSweep time.Time
}

// New returns a Store that writes through client to the state store named in opts.
func New(client StateClient, opts Options) (*Store, error) {
	if client == nil {
		return nil, errors.New("event store: a Dapr state client is required")
	}
	if opts.StoreName == "" {
		return nil, errors.New("event store: a state store name is required")
	}
	ttl := opts.TTL
	switch {
	case ttl == 0:
		ttl = DefaultTTL
	case ttl < 0 && ttl != NoTTL:
		return nil, fmt.Errorf("event store: TTL must not be negative, got %s", opts.TTL)
	case ttl > 0 && (ttl < time.Second || ttl%time.Second != 0):
		// ttlInSeconds takes whole seconds; a shorter TTL would be sent as "0".
		return nil, fmt.Errorf("event store: TTL must be a whole number of seconds, at least 1s, got %s", opts.TTL)
	}
	prefix := opts.KeyPrefix
	if prefix == "" {
		prefix = DefaultKeyPrefix
	}
	return &Store{
		client:     client,
		storeName:  opts.StoreName,
		prefix:     prefix,
		eventMeta:  ttlMetadata(ttl),
		recordMeta: ttlMetadata(recordTTL(ttl)),
		now:        time.Now,
		streams:    make(map[streamRef]*streamState),
		sessions:   make(map[string]*sessionState),
		closed:     newClosedSessions(closedRetention(ttl), closedSessionLimit),
	}, nil
}

func recordTTL(ttl time.Duration) time.Duration {
	if ttl == NoTTL {
		return NoTTL
	}
	return ttl * recordTTLFactor
}

func ttlMetadata(ttl time.Duration) map[string]string {
	if ttl == NoTTL {
		return nil
	}
	return map[string]string{ttlMetadataKey: strconv.Itoa(int(ttl / time.Second))}
}

// Open implements mcp.EventStore. Opening an existing stream keeps its events.
// A state store failure is returned as ErrUnavailable, whose message carries no detail,
// because the SDK writes it into the HTTP response; WithLogging logs the detail.
func (s *Store) Open(ctx context.Context, sessionID, streamID string) error {
	err := s.open(ctx, sessionID, streamID)
	if err == nil || errors.Is(err, ErrSessionClosed) {
		return err
	}
	return &unavailableError{cause: err}
}

func (s *Store) open(ctx context.Context, sessionID, streamID string) error {
	ref := streamRef{session: sessionID, stream: streamID}
	st, err := s.acquire(ref)
	if err != nil {
		return err
	}
	defer s.release(st)
	st.mu.Lock()
	defer st.mu.Unlock()
	if err := s.checkOpen(sessionID); err != nil || st.loaded {
		return err
	}
	// A stream the SDK has just created cannot have a record yet, so only other IDs are read.
	if !IsFreshStreamID(streamID) {
		found, err := s.load(ctx, st, ref)
		if err != nil || found {
			return err
		}
	}
	if err := s.createStream(ctx, sessionID, streamID); err != nil {
		return err
	}
	st.loaded = true
	return nil
}

// freshStreamIDLen and the alphabet below describe crypto/rand.Text, which the SDK uses to
// name the stream of every POST. Every other stream ID passed to Open is the standalone stream "".
const freshStreamIDLen = 26

// IsFreshStreamID reports whether id has the shape of a stream ID the SDK generated with
// crypto/rand.Text: 26 characters of the base32 alphabet, so 130 random bits. Open skips the
// record read for such IDs, which relies on the SDK opening only new streams and the
// standalone stream "". It is exported so tests of the server can check that still holds.
func IsFreshStreamID(id string) bool {
	if len(id) != freshStreamIDLen {
		return false
	}
	for _, c := range id {
		if (c < 'A' || c > 'Z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// createStream writes the records of a new stream: its entry in the session's stream index,
// the session record that counts those entries, and the stream record with a zero count.
// The three writes are independent and run concurrently. The caller holds the stream's lock.
func (s *Store) createStream(ctx context.Context, sessionID, streamID string) error {
	ss := s.session(sessionID)
	ss.mu.Lock()
	defer ss.mu.Unlock()
	n, err := s.loadSession(ctx, ss, sessionID)
	if err != nil {
		return err
	}
	// The slot is used; never hand it out again, even if a write below fails.
	ss.opened = n + 1
	writes := []func() error{
		func() error {
			return s.saveRecord(ctx, streamIndexKey(s.prefix, sessionID, n), streamIndexRecord{Stream: streamID})
		},
		func() error {
			return s.saveRecord(ctx, sessionKey(s.prefix, sessionID), sessionRecord{Streams: n + 1})
		},
		func() error {
			return s.saveRecord(ctx, streamKey(s.prefix, sessionID, streamID), streamRecord{Count: 0})
		},
	}
	errs := make([]error, len(writes))
	var wg sync.WaitGroup
	for i, write := range writes {
		wg.Go(func() { errs[i] = write() })
	}
	wg.Wait()
	return errors.Join(errs...)
}

// Append implements mcp.EventStore.
//
// The SDK numbers events itself and gives each one the next index even when Append
// fails, so Append reserves the index whatever happens. A failed event write leaves a
// hole at that index, which makes After report mcp.ErrEventsPurged for any range that
// covers it, instead of replaying later events under the wrong IDs.
func (s *Store) Append(ctx context.Context, sessionID, streamID string, data []byte) error {
	ref := streamRef{session: sessionID, stream: streamID}
	st, err := s.acquire(ref)
	if err != nil {
		return err
	}
	defer s.release(st)
	st.mu.Lock()
	defer st.mu.Unlock()
	defer st.updatePinned()
	if err = s.checkOpen(sessionID); err != nil {
		return err
	}
	found, err := s.load(ctx, st, ref)
	if err != nil {
		// The count is unknown, so the index the SDK just used cannot be reserved yet.
		// load adds it once the count can be read.
		st.unreserved++
		return err
	}
	if !found {
		return unknownStream(sessionID, streamID)
	}
	index := st.count
	st.count++
	// Writing the count also recreates a stream record that expired while the count stayed cached.
	eventErr := s.save(ctx, eventKey(s.prefix, sessionID, streamID, index), eventRecord{Data: data}, s.eventMeta)
	countErr := s.saveRecord(ctx, streamKey(s.prefix, sessionID, streamID), streamRecord{Count: st.count})
	st.unsaved = countErr != nil
	return errors.Join(eventErr, countErr)
}

// After implements mcp.EventStore. It reads every requested event before yielding any,
// so a missing event fails the whole call with mcp.ErrEventsPurged instead of a partial replay.
//
// As in the SDK's MemoryEventStore, that includes a replay from the start (index -1, the SDK's
// GET without Last-Event-ID) once the stream's first event has expired. The SDK numbers the
// events After yields from index+1, so skipping expired events would give every later event
// an ID too low, and a resume from one of those IDs would send events again.
func (s *Store) After(ctx context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		events, err := s.readAfter(ctx, sessionID, streamID, index)
		if err != nil {
			yield(nil, err)
			return
		}
		for _, e := range events {
			if !yield(e, nil) {
				return
			}
		}
	}
}

func (s *Store) readAfter(ctx context.Context, sessionID, streamID string, index int) ([][]byte, error) {
	count, found, err := s.streamCount(ctx, streamRef{session: sessionID, stream: streamID})
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, unknownStream(sessionID, streamID)
	}
	first := max(index+1, 0)
	if first >= count {
		return nil, nil
	}
	// Events expire oldest first, so the first one requested is the one most likely gone.
	// Reading it alone fails a replay of an old range with one lookup, whatever the stream's length.
	firstKey := eventKey(s.prefix, sessionID, streamID, first)
	var rec eventRecord
	present, err := s.get(ctx, firstKey, &rec)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, fmt.Errorf("event store: %q: %w", firstKey, mcp.ErrEventsPurged)
	}
	keys := make([]string, 0, count-first-1)
	for i := first + 1; i < count; i++ {
		keys = append(keys, eventKey(s.prefix, sessionID, streamID, i))
	}
	values, err := s.getBulk(ctx, keys)
	if err != nil {
		return nil, err
	}
	rest, err := s.decodeEvents(keys, values)
	if err != nil {
		return nil, err
	}
	return append([][]byte{rec.Data}, rest...), nil
}

// decodeEvents decodes the event records read from keys. A missing one fails the whole
// read with mcp.ErrEventsPurged.
func (s *Store) decodeEvents(keys []string, values [][]byte) ([][]byte, error) {
	events := make([][]byte, 0, len(values))
	for i, raw := range values {
		var rec eventRecord
		present, err := decode(raw, &rec)
		if err != nil {
			return nil, fmt.Errorf("event store: %q in state store %q: %w", keys[i], s.storeName, err)
		}
		if !present {
			return nil, fmt.Errorf("event store: %q: %w", keys[i], mcp.ErrEventsPurged)
		}
		events = append(events, rec.Data)
	}
	return events, nil
}

// getBulk reads keys and returns their raw values in the same order; a missing key reads as
// empty. It reads at most maxBulkKeys keys per call.
func (s *Store) getBulk(ctx context.Context, keys []string) ([][]byte, error) {
	values := make([][]byte, 0, len(keys))
	for chunk := range slices.Chunk(keys, maxBulkKeys) {
		read, err := s.getBulkChunk(ctx, chunk)
		if err != nil {
			return nil, err
		}
		values = append(values, read...)
	}
	return values, nil
}

func (s *Store) getBulkChunk(ctx context.Context, keys []string) ([][]byte, error) {
	items, err := s.client.GetBulkState(ctx, s.storeName, keys, nil, bulkParallelism)
	if err != nil {
		return nil, fmt.Errorf("event store: read from state store %q: %w", s.storeName, err)
	}
	byKey := make(map[string][]byte, len(items))
	for _, item := range items {
		if item.Error != "" {
			return nil, fmt.Errorf("event store: read %q from state store %q: %s", item.Key, s.storeName, item.Error)
		}
		byKey[item.Key] = item.Value
	}
	values := make([][]byte, len(keys))
	for i, key := range keys {
		values[i] = byKey[key]
	}
	return values, nil
}

// deleteBulk deletes keys, at most maxBulkKeys per call. A failed call does not stop the
// others, so as much as possible is reclaimed now and the TTL reclaims the rest.
func (s *Store) deleteBulk(ctx context.Context, keys []string) error {
	var errs []error
	for chunk := range slices.Chunk(keys, maxBulkKeys) {
		if err := s.client.DeleteBulkState(ctx, s.storeName, chunk, nil); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Store) get(ctx context.Context, key string, v any) (bool, error) {
	item, err := s.client.GetState(ctx, s.storeName, key, nil)
	if err != nil {
		return false, fmt.Errorf("event store: read %q from state store %q: %w", key, s.storeName, err)
	}
	if item == nil {
		return false, nil
	}
	found, err := decode(item.Value, v)
	if err != nil {
		return found, fmt.Errorf("event store: %q in state store %q: %w", key, s.storeName, err)
	}
	return found, nil
}

// saveRecord saves a bookkeeping record, which outlives the events it describes.
func (s *Store) saveRecord(ctx context.Context, key string, v any) error {
	return s.save(ctx, key, v, s.recordMeta)
}

func (s *Store) save(ctx context.Context, key string, v any, meta map[string]string) error {
	data, err := encode(v)
	if err != nil {
		return fmt.Errorf("event store: %q: %w", key, err)
	}
	if err := s.client.SaveState(ctx, s.storeName, key, data, meta); err != nil {
		return fmt.Errorf("event store: write %q to state store %q: %w", key, s.storeName, err)
	}
	return nil
}

// probeKeySegment names the key Check writes. A path-escaped ID never holds "%" followed
// by a non-hex character, so no record this store writes can share the key.
const probeKeySegment = "%probe"

// Check writes and deletes one key, with the same metadata as an event, to fail fast when the
// state store is missing, unreachable, read-only or rejects ttlInSeconds, instead of failing
// every session later.
func (s *Store) Check(ctx context.Context) error {
	key := joinKey(s.prefix, probeKeySegment)
	if err := s.client.SaveState(ctx, s.storeName, key, []byte("{}"), s.eventMeta); err != nil {
		return fmt.Errorf("event store: state store %q is not usable: write: %w", s.storeName, err)
	}
	if err := s.client.DeleteBulkState(ctx, s.storeName, []string{key}, nil); err != nil {
		return fmt.Errorf("event store: state store %q is not usable: delete: %w", s.storeName, err)
	}
	return nil
}
