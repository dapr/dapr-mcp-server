package eventstore

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

const testStore = "statestore"

var _ mcp.EventStore = (*Store)(nil)

func newTestStore(t *testing.T, fake StateClient) *Store {
	t.Helper()
	s, err := New(fake, Options{StoreName: testStore})
	require.NoError(t, err)
	return s
}

func collect(t *testing.T, s *Store, session, stream string, index int) ([]string, error) {
	t.Helper()
	var out []string
	for data, err := range s.After(context.Background(), session, stream, index) {
		if err != nil {
			return out, err
		}
		out = append(out, string(data))
	}
	return out, nil
}

func appendAll(t *testing.T, s *Store, session, stream string, events ...string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.Open(ctx, session, stream))
	for _, e := range events {
		require.NoError(t, s.Append(ctx, session, stream, []byte(e)))
	}
}

func TestNewValidatesOptions(t *testing.T) {
	t.Parallel()
	_, err := New(nil, Options{StoreName: testStore})
	require.ErrorContains(t, err, "state client")

	_, err = New(mocks.NewFakeStateStore(), Options{})
	require.ErrorContains(t, err, "store name")

	_, err = New(mocks.NewFakeStateStore(), Options{StoreName: testStore, TTL: -time.Second})
	require.ErrorContains(t, err, "TTL")

	// ttlInSeconds takes whole seconds; 500ms would be sent as "0".
	for _, ttl := range []time.Duration{time.Nanosecond, 500 * time.Millisecond, 1500 * time.Millisecond} {
		_, err = New(mocks.NewFakeStateStore(), Options{StoreName: testStore, TTL: ttl})
		require.ErrorContains(t, err, "whole number of seconds", "TTL %s", ttl)
	}
	for _, ttl := range []time.Duration{time.Second, 90 * time.Second, NoTTL} {
		_, err = New(mocks.NewFakeStateStore(), Options{StoreName: testStore, TTL: ttl})
		require.NoError(t, err, "TTL %s", ttl)
	}
}

func TestStoreAndReplay(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "sess", "1", "a", "b", "c")

	tests := []struct {
		name  string
		index int
		want  []string
	}{
		{name: "from the start", index: -1, want: []string{"a", "b", "c"}},
		{name: "after the first event", index: 0, want: []string{"b", "c"}},
		{name: "after the last event", index: 2, want: nil},
		{name: "past the end", index: 10, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := collect(t, s, "sess", "1", tt.index)
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestEmptyEventsSurviveRoundTrip(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	ctx := context.Background()
	require.NoError(t, s.Open(ctx, "sess", "1"))
	// The SDK appends a nil priming event to align indexes.
	require.NoError(t, s.Append(ctx, "sess", "1", nil))
	require.NoError(t, s.Append(ctx, "sess", "1", []byte("x")))

	got, err := collect(t, s, "sess", "1", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "x"}, got)
}

func TestStreamsAndSessionsAreIsolated(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "s1", "1", "s1-1-a", "s1-1-b")
	appendAll(t, s, "s1", "2", "s1-2-a")
	appendAll(t, s, "s2", "1", "s2-1-a")

	got, err := collect(t, s, "s1", "1", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"s1-1-a", "s1-1-b"}, got)

	got, err = collect(t, s, "s1", "2", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"s1-2-a"}, got)

	got, err = collect(t, s, "s2", "1", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"s2-1-a"}, got)
}

func TestSeparatorsInIDsDoNotCollide(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "a/b", "c", "first")
	appendAll(t, s, "a", "b/c", "second")

	got, err := collect(t, s, "a/b", "c", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"first"}, got)
	got, err = collect(t, s, "a", "b/c", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"second"}, got)
}

func TestReplayAfterRestart(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	appendAll(t, newTestStore(t, fake), "sess", "1", "a", "b")

	// A second process sharing the state store sees the stream and continues its numbering.
	restarted := newTestStore(t, fake)
	require.NoError(t, restarted.Open(context.Background(), "sess", "1"))
	require.NoError(t, restarted.Append(context.Background(), "sess", "1", []byte("c")))

	got, err := collect(t, restarted, "sess", "1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"b", "c"}, got)
}

func TestOpenIsIdempotent(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "sess", "1", "a")
	require.NoError(t, s.Open(context.Background(), "sess", "1"))

	got, err := collect(t, s, "sess", "1", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, got, "reopening must not reset the stream")
}

func TestAfterUnknownStream(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	_, err := collect(t, s, "sess", "missing", -1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unknown stream")
}

func TestAfterReportsPurgedEvents(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	appendAll(t, s, "sess", "1", "a", "b", "c")
	fake.Delete(testStore, eventKey(DefaultKeyPrefix, "sess", "1", 1))

	got, err := collect(t, s, "sess", "1", -1)
	require.ErrorIs(t, err, mcp.ErrEventsPurged)
	assert.Empty(t, got, "no partial results when an event is gone")

	got, err = collect(t, s, "sess", "1", 1)
	require.NoError(t, err, "events after the purged one are still complete")
	assert.Equal(t, []string{"c"}, got)
}

func TestSavesCarryTTL(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s, err := New(fake, Options{StoreName: testStore, TTL: 90 * time.Second})
	require.NoError(t, err)
	appendAll(t, s, "sess", "1", "a")

	eventKey := eventKey(DefaultKeyPrefix, "sess", "1", 0)
	for _, key := range fake.Keys(testStore) {
		meta, err := fake.SaveMetadata(testStore, key)
		require.NoError(t, err)
		want := "2160" // 24 times the event TTL
		if key == eventKey {
			want = "90"
		}
		assert.Equal(t, want, meta[ttlMetadataKey], "key %s", key)
	}
}

func TestZeroTTLSendsNoMetadata(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s, err := New(fake, Options{StoreName: testStore, TTL: NoTTL})
	require.NoError(t, err)
	appendAll(t, s, "sess", "1", "a")

	for _, key := range fake.Keys(testStore) {
		meta, err := fake.SaveMetadata(testStore, key)
		require.NoError(t, err)
		assert.NotContains(t, meta, ttlMetadataKey)
	}
}

func TestSessionClosedRemovesEverything(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	appendAll(t, s, "sess", "1", "a", "b")
	appendAll(t, s, "sess", "2", "c")
	appendAll(t, s, "other", "1", "keep")

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))

	for _, key := range fake.Keys(testStore) {
		assert.NotContains(t, key, "/sess", "key %s should have been removed", key)
	}
	_, err := collect(t, s, "sess", "1", -1)
	require.Error(t, err)
	got, err := collect(t, s, "other", "1", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"keep"}, got)
}

func TestSessionClosedUnknownSession(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	require.NoError(t, s.SessionClosed(context.Background(), "never-opened"))
}

func TestStateStoreErrorsSurface(t *testing.T) {
	t.Parallel()
	boom := errors.New("state store unavailable")
	ctx := context.Background()

	tests := []struct {
		name   string
		inject func(*mocks.FakeStateStore)
		call   func(*Store) error
		// opaque marks Open, whose error reaches the HTTP response without detail.
		opaque bool
	}{
		{
			name:   "open read",
			inject: func(f *mocks.FakeStateStore) { f.GetErr = boom },
			call:   func(s *Store) error { return s.Open(ctx, "fresh", "1") },
			opaque: true,
		},
		{
			name:   "open write",
			inject: func(f *mocks.FakeStateStore) { f.SaveErr = boom },
			call:   func(s *Store) error { return s.Open(ctx, "fresh", "1") },
			opaque: true,
		},
		{
			name:   "append",
			inject: func(f *mocks.FakeStateStore) { f.SaveErr = boom },
			call:   func(s *Store) error { return s.Append(ctx, "sess", "1", []byte("x")) },
		},
		{
			name:   "after, first event",
			inject: func(f *mocks.FakeStateStore) { f.GetErr = boom },
			call: func(s *Store) error {
				for _, err := range s.After(ctx, "sess", "1", -1) {
					return err
				}
				return nil
			},
		},
		{
			name:   "after, later events",
			inject: func(f *mocks.FakeStateStore) { f.BulkGetErr = boom },
			call: func(s *Store) error {
				for _, err := range s.After(ctx, "sess", "1", -1) {
					return err
				}
				return nil
			},
		},
		{
			name:   "after on a stream this process has not seen",
			inject: func(f *mocks.FakeStateStore) { f.GetErr = boom },
			call: func(s *Store) error {
				for _, err := range s.After(ctx, "other-session", "1", -1) {
					return err
				}
				return nil
			},
		},
		{
			name:   "session closed read",
			inject: func(f *mocks.FakeStateStore) { f.GetErr = boom },
			call:   func(s *Store) error { return s.SessionClosed(ctx, "sess") },
		},
		{
			name:   "session closed delete",
			inject: func(f *mocks.FakeStateStore) { f.BulkDeleteErr = boom },
			call:   func(s *Store) error { return s.SessionClosed(ctx, "sess") },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := mocks.NewFakeStateStore()
			s := newTestStore(t, fake)
			// Two events, so a replay reads the first alone and the second in bulk.
			appendAll(t, s, "sess", "1", "a", "b")
			appendAll(t, newTestStore(t, fake), "other-session", "1", "c")
			tt.inject(fake)

			err := tt.call(s)
			require.ErrorIs(t, err, boom)
			if tt.opaque {
				require.ErrorIs(t, err, ErrUnavailable)
				assert.Equal(t, "event store unavailable", err.Error(), "no key, store name or cause")
				return
			}
			assert.True(t, strings.Contains(err.Error(), testStore), "error should name the store: %v", err)
		})
	}
}

func TestBulkItemErrorSurfaces(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, &itemErrorClient{FakeStateStore: mocks.NewFakeStateStore()})
	// The first event is read alone, the rest in bulk.
	appendAll(t, s, "sess", "1", "a", "b")

	_, err := collect(t, s, "sess", "1", -1)
	require.ErrorContains(t, err, "item failed")
}

func TestAfterChecksTheFirstEventBeforeReadingTheRest(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	counting := &getCountingClient{FakeStateStore: fake}
	s := newTestStore(t, counting)
	appendAll(t, s, "sess", "1", "a", "b", "c", "d")
	expire(fake, eventKey(DefaultKeyPrefix, "sess", "1", 0), eventKey(DefaultKeyPrefix, "sess", "1", 1))
	getsBefore := counting.gets.Load()
	bulkBefore, _ := fake.BulkCalls()

	_, err := collect(t, s, "sess", "1", 0)
	require.ErrorIs(t, err, mcp.ErrEventsPurged)
	bulkAfter, _ := fake.BulkCalls()
	assert.Equal(t, int64(1), counting.gets.Load()-getsBefore, "one read of the first event requested")
	assert.Len(t, bulkAfter, len(bulkBefore), "no bulk read once the first event is gone")

	got, err := collect(t, s, "sess", "1", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"c", "d"}, got)
}

// getCountingClient counts GetState calls.
type getCountingClient struct {
	*mocks.FakeStateStore
	gets atomic.Int64
}

func (c *getCountingClient) GetState(ctx context.Context, storeName, key string, meta map[string]string) (*client.StateItem, error) {
	c.gets.Add(1)
	return c.FakeStateStore.GetState(ctx, storeName, key, meta)
}

// itemErrorClient reports a per-item error from GetBulkState, as Dapr does when one key fails.
type itemErrorClient struct {
	*mocks.FakeStateStore
}

func (c *itemErrorClient) GetBulkState(ctx context.Context, storeName string, keys []string, meta map[string]string, parallelism int32) ([]*client.BulkStateItem, error) {
	items, err := c.FakeStateStore.GetBulkState(ctx, storeName, keys, meta, parallelism)
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		item.Error = "item failed"
	}
	return items, nil
}

func TestFailedAppendKeepsIndicesAligned(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	failing := &failKeyOnceClient{FakeStateStore: fake, key: eventKey(DefaultKeyPrefix, "sess", "1", 1)}
	s := newTestStore(t, failing)
	ctx := context.Background()
	require.NoError(t, s.Open(ctx, "sess", "1"))
	// The SDK numbers events itself and gives each the next index whether or not Append
	// succeeded: prime = 0, "lost" = 1, "kept" = 2, "next" = 3.
	require.NoError(t, s.Append(ctx, "sess", "1", nil))
	require.Error(t, s.Append(ctx, "sess", "1", []byte("lost")))
	require.NoError(t, s.Append(ctx, "sess", "1", []byte("kept")))
	require.NoError(t, s.Append(ctx, "sess", "1", []byte("next")))

	got, err := collect(t, s, "sess", "1", 2)
	require.NoError(t, err)
	assert.Equal(t, []string{"next"}, got, "a client that saw event 2 resumes with event 3")

	_, err = collect(t, s, "sess", "1", 0)
	require.ErrorIs(t, err, mcp.ErrEventsPurged, "a range over the failed event must fail, not shift")
}

// failKeyOnceClient fails the first save of one key.
type failKeyOnceClient struct {
	*mocks.FakeStateStore
	key    string
	failed bool
}

func (c *failKeyOnceClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	if key == c.key && !c.failed {
		c.failed = true
		return errors.New("transient save failure")
	}
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

func TestAppendBeforeCountIsKnownStillUsesAnIndex(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	appendAll(t, newTestStore(t, fake), "sess", "1", "a")

	// A process that has not read the stream yet cannot read its count.
	s := newTestStore(t, fake)
	ctx := context.Background()
	fake.SetGetErr(errors.New("state store unavailable"))
	require.Error(t, s.Append(ctx, "sess", "1", []byte("lost")))
	fake.SetGetErr(nil)
	require.NoError(t, s.Append(ctx, "sess", "1", []byte("next")))

	got, err := collect(t, s, "sess", "1", 1)
	require.NoError(t, err)
	assert.Equal(t, []string{"next"}, got, "the event the SDK numbered 2 is stored at index 2")
	_, err = collect(t, s, "sess", "1", 0)
	require.ErrorIs(t, err, mcp.ErrEventsPurged)
}

func TestAppendToExpiredStreamFails(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	appendAll(t, newTestStore(t, fake), "sess", "1", "a")
	fake.Delete(testStore, streamKey(DefaultKeyPrefix, "sess", "1"))

	err := newTestStore(t, fake).Append(context.Background(), "sess", "1", []byte("b"))
	require.ErrorContains(t, err, "unknown stream")
}

func TestAfterOnUnknownStreamsKeepsNoState(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "sess", "1", "a")
	s.mu.Lock()
	streams, sessions := len(s.streams), s.sessions["sess"].opened
	s.mu.Unlock()

	for i := range 100 {
		_, err := collect(t, s, "sess", "client-chosen-"+strconv.Itoa(i), -1)
		require.ErrorContains(t, err, "unknown stream")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.streams, streams, "a Last-Event-ID for an unknown stream must not add state")
	assert.Equal(t, sessions, s.sessions["sess"].opened)
}

func TestAfterRejectsStreamIDsThatAliasOtherRecords(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	appendAll(t, s, "sess", "a", "x", "y")
	before := fake.Keys(testStore)

	// Stream IDs reach After from the client's Last-Event-ID.
	for _, id := range []string{"a/0", "a/1", "a#0", "../x", "../sess/a", "/a", "a/"} {
		got, err := collect(t, s, "sess", id, -1)
		require.ErrorContains(t, err, "unknown stream", "stream ID %q", id)
		assert.Empty(t, got, "stream ID %q", id)
	}
	assert.Equal(t, before, fake.Keys(testStore), "a crafted stream ID must not add or alias records")
}
