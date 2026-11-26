package eventstore

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

// streamsPerSession stands in for a long-lived session: the SDK opens one stream per request.
const streamsPerSession = 200

func TestSessionRecordStaysSmallAsStreamsGrow(t *testing.T) {
	t.Parallel()
	sizes := &sizeRecordingClient{FakeStateStore: mocks.NewFakeStateStore(), key: sessionKey(DefaultKeyPrefix, "sess")}
	s := newTestStore(t, sizes)
	for i := range streamsPerSession {
		appendAll(t, s, "sess", strconv.Itoa(i), "e")
	}

	require.Len(t, sizes.sizes, streamsPerSession, "one session record write per stream")
	assert.LessOrEqual(t, sizes.max(), 2*sizes.sizes[0],
		"the session record must not grow with the number of streams")
}

func TestSessionClosedRemovesEveryStreamOfALongSession(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	for i := range streamsPerSession {
		appendAll(t, s, "sess", strconv.Itoa(i), "e")
	}

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))
	assert.Empty(t, fake.Keys(testStore))
}

func TestSessionClosedFindsStreamsOpenedByAnotherStore(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	appendAll(t, newTestStore(t, fake), "sess", "1", "a")
	s := newTestStore(t, fake)
	appendAll(t, s, "sess", "2", "b")

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))
	assert.Empty(t, fake.Keys(testStore))
}

func TestIdleStreamsAreEvictedAndReloaded(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	for i := range streamsPerSession {
		appendAll(t, s, "sess", strconv.Itoa(i), "e")
	}
	require.Len(t, s.streams, streamsPerSession)

	clock.advance(streamIdleTimeout + streamSweepInterval)
	appendAll(t, s, "sess", "new", "f")
	assert.Len(t, s.streams, 1, "idle, persisted streams must leave the cache")

	require.NoError(t, s.Append(context.Background(), "sess", "0", []byte("g")))
	got, err := collect(t, s, "sess", "0", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"e", "g"}, got, "an evicted stream continues from its stored count")
}

func TestUnsavedStreamsAreNotEvicted(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	failing := &failKeyOnceClient{FakeStateStore: fake, key: streamKey(DefaultKeyPrefix, "sess", "1")}
	s := newTestStore(t, fake)
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	appendAll(t, s, "sess", "1", "a")
	s.client = failing
	require.Error(t, s.Append(context.Background(), "sess", "1", []byte("b")))
	s.client = fake

	clock.advance(streamIdleTimeout + streamSweepInterval)
	appendAll(t, s, "sess", "2", "c")
	_, cached := s.streams[streamRef{session: "sess", stream: "1"}]
	assert.True(t, cached, "a count the stream record lacks must stay in memory")
}

// sizeRecordingClient records the size of every write to one key.
type sizeRecordingClient struct {
	*mocks.FakeStateStore
	key   string
	sizes []int
}

func (c *sizeRecordingClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	if key == c.key {
		c.sizes = append(c.sizes, len(data))
	}
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

func (c *sizeRecordingClient) max() int {
	m := 0
	for _, n := range c.sizes {
		m = max(m, n)
	}
	return m
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// expire deletes keys as the state store's TTL would.
func expire(fake *mocks.FakeStateStore, keys ...string) {
	for _, key := range keys {
		fake.Delete(testStore, key)
	}
}

func TestStandaloneStreamOutlivesItsRecords(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	ctx := context.Background()
	appendAll(t, s, "sess", standaloneStream, "a")

	// The session stays active through requests, but its standalone stream sends
	// nothing until every record it wrote has expired.
	clock.advance(DefaultTTL + time.Minute)
	expire(fake, fake.Keys(testStore)...)
	appendAll(t, s, "sess", "request", "r")

	require.NoError(t, s.Append(ctx, "sess", standaloneStream, []byte("b")))
	got, err := collect(t, s, "sess", standaloneStream, 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, got, "the stream keeps its numbering")
	_, err = collect(t, s, "sess", standaloneStream, -1)
	require.ErrorIs(t, err, mcp.ErrEventsPurged, "a replay from the start cannot begin after an expired event")

	require.NoError(t, s.SessionClosed(ctx, "sess"))
	assert.Empty(t, fake.Keys(testStore), "the recreated records are removed with the session")
}

func TestSilentRequestStreamOutlivesItsEvents(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	appendAll(t, s, "sess", "call", "progress")

	// A long tool call sends nothing for longer than the event TTL; its cached count
	// is evicted and its events expire, but its stream record lives longer.
	clock.advance(DefaultTTL + time.Minute)
	expire(fake, eventKey(DefaultKeyPrefix, "sess", "call", 0))
	appendAll(t, s, "sess", "other", "o")

	require.NoError(t, s.Append(context.Background(), "sess", "call", []byte("result")))
	got, err := collect(t, s, "sess", "call", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"result"}, got)
}

func TestFreshReplayFailsOnceTheFirstEventExpired(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	// The SDK opens the standalone stream at session start and stores notifications on it
	// whether or not a GET is attached.
	appendAll(t, s, "sess", standaloneStream, "a", "b")
	clock.advance(DefaultTTL + time.Minute)
	expire(fake, eventKey(DefaultKeyPrefix, "sess", standaloneStream, 0))
	require.NoError(t, s.Append(context.Background(), "sess", standaloneStream, []byte("c")))

	// A GET without Last-Event-ID replays from index -1, and the SDK numbers what After
	// yields from 0. Starting at the oldest kept event would send "b" as event 0.
	got, err := collect(t, s, "sess", standaloneStream, -1)
	require.ErrorIs(t, err, mcp.ErrEventsPurged, "as the SDK's MemoryEventStore does")
	assert.Empty(t, got)

	got, err = collect(t, s, "sess", standaloneStream, 0)
	require.NoError(t, err, "a client that saw event 0 resumes")
	assert.Equal(t, []string{"b", "c"}, got)
}
