package eventstore

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

// bulkLimit is the most keys one bulk call may carry to stay under Dapr's default 4MB
// gRPC message size with room to spare.
const bulkLimit = 500

func TestSessionClosedChunksBulkCalls(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	const streams = bulkLimit + 20
	for i := range streams {
		appendAll(t, s, "sess", strconv.Itoa(i), "a", "b")
	}

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))
	assert.Empty(t, fake.Keys(testStore))

	gets, deletes := fake.BulkCalls()
	// The index entries and the stream records each need two reads.
	assert.Equal(t, []int{bulkLimit, 20, bulkLimit, 20}, gets)
	// The session record, then per stream an index entry, a record and two events.
	total := 1 + streams*4
	require.Len(t, deletes, (total+bulkLimit-1)/bulkLimit)
	sum := 0
	for _, n := range deletes {
		assert.LessOrEqual(t, n, bulkLimit)
		sum += n
	}
	assert.Equal(t, total, sum)
}

func TestSessionClosedKeepsDeletingAfterAFailedChunk(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	failing := &failFirstDeleteClient{FakeStateStore: fake}
	s := newTestStore(t, failing)
	for i := range bulkLimit {
		appendAll(t, s, "sess", strconv.Itoa(i), "a")
	}

	require.ErrorContains(t, s.SessionClosed(context.Background(), "sess"), "first delete fails")
	_, deletes := fake.BulkCalls()
	assert.Len(t, deletes, 4, "every chunk is tried")
}

// failFirstDeleteClient fails the first DeleteBulkState call.
type failFirstDeleteClient struct {
	*mocks.FakeStateStore
	failed bool
}

func (c *failFirstDeleteClient) DeleteBulkState(ctx context.Context, storeName string, keys []string, meta map[string]string) error {
	err := c.FakeStateStore.DeleteBulkState(ctx, storeName, keys, meta)
	if !c.failed {
		c.failed = true
		return errors.New("first delete fails")
	}
	return err
}

func TestSessionClosedDeletesEventsWhoseCountWriteFailed(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	appendAll(t, s, "sess", "1", "a")
	// The event write succeeds but the count write does not: the stored count stays 1.
	s.client = &failKeyOnceClient{FakeStateStore: fake, key: streamKey(DefaultKeyPrefix, "sess", "1")}
	require.Error(t, s.Append(context.Background(), "sess", "1", []byte("b")))
	s.client = fake

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))
	assert.Empty(t, fake.Keys(testStore), "the event past the stored count must be deleted too")
}

func TestClosedSessionsRefuseWrites(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	s := newTestStore(t, fake)
	ctx := context.Background()
	appendAll(t, s, "sess", "1", "a")
	require.NoError(t, s.SessionClosed(ctx, "sess"))

	require.ErrorIs(t, s.Open(ctx, "sess", "2"), ErrSessionClosed)
	require.ErrorIs(t, s.Append(ctx, "sess", "1", []byte("b")), ErrSessionClosed)
	assert.Empty(t, fake.Keys(testStore), "a closed session's records stay deleted")
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Empty(t, s.streams, "a refused write keeps no state")
	assert.Empty(t, s.sessions)
}

func TestSessionClosedWaitsForAWriteUnderWay(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	blocking := &blockingSaveClient{FakeStateStore: fake, target: eventKey(DefaultKeyPrefix, "sess", "1", 1)}
	s := newTestStore(t, blocking)
	ctx := context.Background()
	appendAll(t, s, "sess", "1", "a")
	blocking.arm()

	appended := make(chan error, 1)
	go func() { appended <- s.Append(ctx, "sess", "1", []byte("b")) }()
	blocking.waitBlocked(t)

	closed := make(chan error, 1)
	go func() { closed <- s.SessionClosed(ctx, "sess") }()
	// Start the window only once SessionClosed has marked the session closed, so it cannot
	// pass merely because SessionClosed has not started yet.
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closed.contains("sess", s.now())
	}, lockTestTimeout, time.Millisecond, "SessionClosed never marked the session closed")
	// Without waiting for the write, SessionClosed would finish now and the write would
	// recreate the records after the delete.
	var closeErr error
	select {
	case closeErr = <-closed:
		t.Error("SessionClosed returned while a write of the session was under way")
		blocking.release()
	case <-time.After(50 * time.Millisecond):
		blocking.release()
		closeErr = <-closed
	}
	require.NoError(t, <-appended)
	require.NoError(t, closeErr)
	assert.Empty(t, fake.Keys(testStore), "the write under way is deleted with the session")
}

// blockingSaveClient blocks the first save of target, once armed, until release.
type blockingSaveClient struct {
	*mocks.FakeStateStore
	target  string
	blocked chan struct{}
	done    chan struct{}
}

func (c *blockingSaveClient) arm() {
	c.blocked = make(chan struct{}, 1)
	c.done = make(chan struct{})
}

func (c *blockingSaveClient) release() { close(c.done) }

func (c *blockingSaveClient) waitBlocked(t *testing.T) {
	t.Helper()
	select {
	case <-c.blocked:
	case <-time.After(lockTestTimeout):
		t.Fatal("the write never started")
	}
}

func (c *blockingSaveClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	if c.blocked != nil && key == c.target {
		select {
		case c.blocked <- struct{}{}:
			<-c.done
		default:
		}
	}
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

func TestSessionClosedForgetsOnlyItsOwnStreams(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "sess", "1", "a")
	appendAll(t, s, "sess", "2", "b")
	appendAll(t, s, "other", "1", "c")

	require.NoError(t, s.SessionClosed(context.Background(), "sess"))
	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.streams, 1)
	assert.Contains(t, s.streams, streamRef{session: "other", stream: "1"})
	assert.Len(t, s.sessions["other"].cached, 1)
}

func TestEvictedStreamsLeaveTheirSession(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	clock := &fakeClock{now: time.Unix(0, 0)}
	s.now = clock.Now
	appendAll(t, s, "sess", "1", "a")
	clock.advance(streamIdleTimeout + streamSweepInterval)
	appendAll(t, s, "sess", "2", "b")

	s.mu.Lock()
	defer s.mu.Unlock()
	assert.Len(t, s.sessions["sess"].cached, 1, "the session lists only its cached streams")
}

func TestClosedSessionsAreBoundedAndExpire(t *testing.T) {
	t.Parallel()
	start := time.Unix(0, 0)
	c := newClosedSessions(time.Hour, 2)
	c.add("a", start)
	c.add("b", start.Add(time.Minute))
	assert.True(t, c.contains("a", start.Add(time.Minute)))

	c.add("c", start.Add(2*time.Minute))
	assert.False(t, c.contains("a", start.Add(2*time.Minute)), "the oldest is forgotten past the limit")
	assert.True(t, c.contains("b", start.Add(2*time.Minute)))
	assert.Len(t, c.closedAt, 2)

	later := start.Add(time.Hour + time.Minute)
	assert.False(t, c.contains("b", later), "a closed session is forgotten after the retention")
	c.add("d", later)
	assert.Equal(t, []string{"c", "d"}, c.order)
	assert.Len(t, c.closedAt, 2)

	assert.Equal(t, recordTTL(DefaultTTL), closedRetention(DefaultTTL))
	assert.Equal(t, DefaultTTL*recordTTLFactor, closedRetention(NoTTL))
}
