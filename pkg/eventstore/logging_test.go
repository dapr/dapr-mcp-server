package eventstore

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestCheck(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	meta := &metaRecordingClient{FakeStateStore: fake}
	s := newTestStore(t, meta)
	require.NoError(t, s.Check(context.Background()))
	assert.Empty(t, fake.Keys(testStore), "the probe key is deleted")
	require.Len(t, meta.saves, 1, "the check writes one key")
	assert.Equal(t, s.eventMeta, meta.saves[0], "the probe is written like an event, TTL included")
}

func TestCheckRejectsAnUnusableStore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		inject func(*mocks.FakeStateStore)
		client func(*mocks.FakeStateStore) StateClient
		want   string
	}{
		{
			name:   "missing store",
			inject: func(f *mocks.FakeStateStore) { f.SaveErr = errors.New("state store nosuchstore is not found") },
			want:   "nosuchstore is not found",
		},
		{
			name:   "read-only store",
			inject: func(f *mocks.FakeStateStore) { f.BulkDeleteErr = errors.New("delete refused") },
			want:   "delete refused",
		},
		{
			name:   "store without TTL support",
			client: func(f *mocks.FakeStateStore) StateClient { return &ttlRejectingClient{FakeStateStore: f} },
			want:   "ttlInSeconds not supported",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := mocks.NewFakeStateStore()
			var client StateClient = fake
			if tt.inject != nil {
				tt.inject(fake)
			}
			if tt.client != nil {
				client = tt.client(fake)
			}
			err := newTestStore(t, client).Check(context.Background())
			require.ErrorContains(t, err, "is not usable")
			require.ErrorContains(t, err, tt.want)
		})
	}
}

// metaRecordingClient records the metadata of every save.
type metaRecordingClient struct {
	*mocks.FakeStateStore
	saves []map[string]string
}

func (c *metaRecordingClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	c.saves = append(c.saves, meta)
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

// ttlRejectingClient refuses saves that carry ttlInSeconds, as a state store without TTL support does.
type ttlRejectingClient struct {
	*mocks.FakeStateStore
}

func (c *ttlRejectingClient) SaveState(ctx context.Context, storeName, key string, data []byte, meta map[string]string, so ...client.StateOption) error {
	if _, ok := meta[ttlMetadataKey]; ok {
		return errors.New("ttlInSeconds not supported")
	}
	return c.FakeStateStore.SaveState(ctx, storeName, key, data, meta, so...)
}

func TestWithLoggingLogsEveryFailure(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	var buf bytes.Buffer
	store := WithLogging(newTestStore(t, fake), slog.New(slog.NewTextHandler(&buf, nil)))
	ctx := context.Background()

	require.NoError(t, store.Open(ctx, "sess", "1"))
	require.NoError(t, store.Append(ctx, "sess", "1", []byte("a")))
	for _, err := range store.After(ctx, "sess", "1", -1) {
		require.NoError(t, err)
	}
	assert.Empty(t, buf.String(), "successes are not logged")

	boom := errors.New("boom")
	fake.SaveErr = boom
	require.ErrorIs(t, store.Open(ctx, "sess", "2"), boom)
	require.ErrorIs(t, store.Append(ctx, "sess", "1", []byte("b")), boom)
	fake.BulkGetErr = boom
	for _, err := range store.After(ctx, "sess", "1", -1) {
		require.ErrorIs(t, err, boom)
	}
	require.ErrorIs(t, store.SessionClosed(ctx, "sess"), boom)

	out := buf.String()
	for _, op := range []string{"open stream", "store event", "replay events", "delete session"} {
		assert.Contains(t, out, "operation=\""+op+"\"")
	}
	assert.Contains(t, out, "level=ERROR")
	assert.NotContains(t, out, "level=DEBUG")
}

func TestWithLoggingLogsOpenDetailItLeavesOutOfTheError(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	var buf bytes.Buffer
	store := WithLogging(newTestStore(t, fake), slog.New(slog.NewTextHandler(&buf, nil)))
	fake.SaveErr = errors.New("rpc error: connection refused")

	err := store.Open(context.Background(), "sess", "1")
	require.ErrorIs(t, err, ErrUnavailable)
	assert.Equal(t, "event store unavailable", err.Error())
	assert.Contains(t, buf.String(), "connection refused")
	assert.Contains(t, buf.String(), testStore)
}

func TestWithLoggingLogsClientCausedReplayErrorsAtDebug(t *testing.T) {
	t.Parallel()
	fake := mocks.NewFakeStateStore()
	var buf bytes.Buffer
	store := WithLogging(newTestStore(t, fake), slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ctx := context.Background()
	require.NoError(t, store.Open(ctx, "sess", "1"))
	for _, e := range []string{"a", "b"} {
		require.NoError(t, store.Append(ctx, "sess", "1", []byte(e)))
	}
	fake.Delete(testStore, eventKey(DefaultKeyPrefix, "sess", "1", 1))

	for _, err := range store.After(ctx, "sess", "unknown", 3) {
		require.ErrorIs(t, err, errUnknownStream)
	}
	for _, err := range store.After(ctx, "sess", "1", 0) {
		require.ErrorIs(t, err, mcp.ErrEventsPurged)
	}

	out := buf.String()
	assert.Equal(t, 2, strings.Count(out, "level=DEBUG"), out)
	assert.NotContains(t, out, "level=ERROR", "a client's Last-Event-ID is not a server failure")
}

func TestWithLoggingStopsWhenTheConsumerStops(t *testing.T) {
	t.Parallel()
	s := newTestStore(t, mocks.NewFakeStateStore())
	appendAll(t, s, "sess", "1", "a", "b")
	store := WithLogging(s, slog.New(slog.DiscardHandler))
	var got []string
	for data, err := range store.After(context.Background(), "sess", "1", -1) {
		require.NoError(t, err)
		got = append(got, string(data))
		break
	}
	assert.Equal(t, []string{"a"}, got)
}
