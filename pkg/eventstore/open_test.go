package eventstore

import (
	"context"
	"crypto/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestIsFreshStreamID(t *testing.T) {
	t.Parallel()
	assert.True(t, IsFreshStreamID(rand.Text()), "the SDK names request streams with crypto/rand.Text")
	for _, id := range []string{"", "1", "abcdefghijklmnopqrstuvwxyz", "ABCDEFGHIJKLMNOPQRSTUVWXY1", "ABCDEFGHIJKLMNOPQRSTUVWXYZ2"} {
		assert.False(t, IsFreshStreamID(id), "%q", id)
	}
}

func TestOpenOfAFreshStreamWritesConcurrentlyWithoutReading(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	calls := &callCountingClient{FakeStateStore: fake}
	s := newTestStore(t, calls)
	ctx := context.Background()
	require.NoError(t, s.Open(ctx, "sess", standaloneStream))
	calls.reset()

	// The three writes of a new stream must all be in flight at once.
	calls.barrier(3)
	streamID := rand.Text()
	require.NoError(t, s.Open(ctx, "sess", streamID))
	assert.Zero(t, calls.gets.Load(), "a stream the SDK just created has no record to read")
	assert.Equal(t, int32(3), calls.saves.Load(), "index entry, session record and stream record")

	require.NoError(t, s.Append(ctx, "sess", streamID, []byte("a")))
	got, err := collect(t, s, "sess", streamID, -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, got)
	require.NoError(t, s.SessionClosed(ctx, "sess"))
	assert.Empty(t, fake.Keys(testStore), "the stream is indexed like any other")
}

func TestOpenStillReadsTheStandaloneStream(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	appendAll(t, newTestStore(t, fake), "sess", standaloneStream, "a")

	s := newTestStore(t, fake)
	require.NoError(t, s.Open(context.Background(), "sess", standaloneStream))
	got, err := collect(t, s, "sess", standaloneStream, -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, got, "reopening the standalone stream keeps its events")
}

// callCountingClient counts reads and writes. Once a barrier is set, each save waits until
// that many saves are in flight, so a test fails if they run one after another.
type callCountingClient struct {
	*mocks.FakeStateStore
	gets, saves atomic.Int32

	mu      sync.Mutex
	waiting int
	want    int
	release chan struct{}
}

func (c *callCountingClient) reset() {
	c.gets.Store(0)
	c.saves.Store(0)
}

func (c *callCountingClient) barrier(n int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.want, c.waiting, c.release = n, 0, make(chan struct{})
}

func (c *callCountingClient) GetState(ctx context.Context, storeName, key string, meta map[string]string) (*client.StateItem, error) {
	c.gets.Add(1)
	return c.FakeStateStore.GetState(ctx, storeName, key, meta)
}

func (c *callCountingClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	c.saves.Add(1)
	c.wait(ctx)
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

func (c *callCountingClient) wait(ctx context.Context) {
	c.mu.Lock()
	release := c.release
	if release == nil {
		c.mu.Unlock()
		return
	}
	c.waiting++
	if c.waiting == c.want {
		close(release)
		c.release = nil
	}
	c.mu.Unlock()
	select {
	case <-release:
	case <-ctx.Done():
	case <-time.After(lockTestTimeout):
		panic("the writes of a new stream did not run concurrently")
	}
}
