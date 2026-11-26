package eventstore

import (
	"context"
	"testing"
	"time"

	"github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

const lockTestTimeout = 5 * time.Second

func TestSessionClosedDoesNotBlockOtherSessions(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	blocking := &blockingClient{FakeStateStore: fake, session: sessionKey(DefaultKeyPrefix, "closing")}
	s := newTestStore(t, blocking)
	appendAll(t, s, "closing", "1", "a")
	appendAll(t, s, "busy", "1", "b")
	blocking.arm()

	closed := make(chan error, 1)
	go func() { closed <- s.SessionClosed(context.Background(), "closing") }()
	blocking.waitBlocked(t)

	done := make(chan error, 1)
	go func() {
		ctx := context.Background()
		if err := s.Open(ctx, "busy", "2"); err != nil {
			done <- err
			return
		}
		if err := s.Append(ctx, "busy", "1", []byte("c")); err != nil {
			done <- err
			return
		}
		_, err := collect(t, s, "busy", "1", -1)
		done <- err
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(lockTestTimeout):
		t.Fatal("a slow SessionClosed blocked another session's writes")
	}

	blocking.release()
	require.NoError(t, <-closed)
}

func TestSessionClosedBoundsItsStateStoreCalls(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	blocking := &blockingClient{FakeStateStore: fake, session: sessionKey(DefaultKeyPrefix, "sess")}
	s := newTestStore(t, blocking)
	appendAll(t, s, "sess", "1", "a")
	blocking.arm()
	defer blocking.release()

	// The SDK calls SessionClosed with context.TODO(), which has no deadline.
	go func() { _ = s.SessionClosed(context.TODO(), "sess") }()
	ctx := blocking.waitBlocked(t)

	deadline, ok := ctx.Deadline()
	require.True(t, ok, "SessionClosed must bound its state store calls")
	assert.WithinDuration(t, time.Now().Add(sessionCloseTimeout), deadline, time.Second,
		"the bound is sessionCloseTimeout from the start of the call")
}

// blockingClient blocks the first read of one session's record, once armed,
// until release is called or the call's context ends.
type blockingClient struct {
	*mocks.FakeStateStore
	session string
	armed   chan struct{}
	blocked chan context.Context
	done    chan struct{}
}

func (c *blockingClient) arm() {
	c.blocked = make(chan context.Context, 1)
	c.done = make(chan struct{})
	c.armed = make(chan struct{})
	close(c.armed)
}

func (c *blockingClient) release() { close(c.done) }

func (c *blockingClient) waitBlocked(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-c.blocked:
		return ctx
	case <-time.After(lockTestTimeout):
		t.Fatal("SessionClosed never read the session record")
		return nil
	}
}

func (c *blockingClient) GetState(ctx context.Context, storeName, key string, meta map[string]string) (*client.StateItem, error) {
	if c.armed != nil && key == c.session {
		select {
		case c.blocked <- ctx:
			select {
			case <-c.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		default:
		}
	}
	return c.FakeStateStore.GetState(ctx, storeName, key, meta)
}
