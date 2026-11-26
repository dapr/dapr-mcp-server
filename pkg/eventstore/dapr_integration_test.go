package eventstore

import (
	"context"
	"crypto/rand"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/daprit"
)

const (
	// integrationTTL outlives the round-trip test.
	integrationTTL = 5 * time.Minute
	// shortTTL is short enough to wait out: events expire after it, records 24 times later.
	shortTTL = 2 * time.Second
)

func TestDaprStateStoreRoundTrip(t *testing.T) {
	client, storeName := daprit.StateClient(t)
	ctx := t.Context()
	s, err := New(client, Options{StoreName: storeName, TTL: integrationTTL})
	require.NoError(t, err)
	require.NoError(t, s.Check(ctx))
	session := "it-" + rand.Text()
	t.Cleanup(func() { _ = s.SessionClosed(context.Background(), session) })

	require.NoError(t, s.Open(ctx, session, "1"))
	for _, e := range []string{"a", "", "c"} {
		require.NoError(t, s.Append(ctx, session, "1", []byte(e)))
	}
	require.NoError(t, s.Open(ctx, session, "2"))
	require.NoError(t, s.Append(ctx, session, "2", []byte("other")))

	got, err := collect(t, s, session, "1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"", "c"}, got, "an empty event is not mistaken for a missing one")

	// A second process reads the same records.
	restarted, err := New(client, Options{StoreName: storeName, TTL: integrationTTL})
	require.NoError(t, err)
	got, err = collect(t, restarted, session, "2", -1)
	require.NoError(t, err)
	assert.Equal(t, []string{"other"}, got)
	_, err = collect(t, restarted, session, "missing", -1)
	require.ErrorContains(t, err, "unknown stream")

	keys, err := s.sessionKeys(ctx, s.session(session), session, nil)
	require.NoError(t, err)
	require.Len(t, keys, 1+2+2+4, "session record, 2 index entries, 2 stream records, 4 events")
	require.NoError(t, s.SessionClosed(ctx, session))
	for _, key := range keys {
		item, err := client.GetState(ctx, storeName, key, nil)
		require.NoError(t, err)
		assert.Empty(t, item.Value, "key %s must be gone after SessionClosed", key)
	}
}

func TestDaprStateStoreCheckRejectsAMissingStore(t *testing.T) {
	client, _ := daprit.StateClient(t)
	s, err := New(client, Options{StoreName: "no-such-store-" + rand.Text()})
	require.NoError(t, err)
	require.ErrorContains(t, s.Check(t.Context()), "is not usable")
}

// The sidecar does not report expiry times, so this test waits for the events to expire.
func TestDaprStateStoreHonoursTTL(t *testing.T) {
	client, storeName := daprit.StateClient(t)
	ctx := t.Context()
	s, err := New(client, Options{StoreName: storeName, TTL: shortTTL})
	require.NoError(t, err)
	session := "it-" + rand.Text()
	t.Cleanup(func() { _ = s.SessionClosed(context.Background(), session) })
	require.NoError(t, s.Open(ctx, session, "1"))
	require.NoError(t, s.Append(ctx, session, "1", []byte("a")))

	time.Sleep(shortTTL + time.Second)

	_, err = collect(t, s, session, "1", -1)
	require.ErrorIs(t, err, mcp.ErrEventsPurged, "a replay from the start cannot skip the expired event")
	fresh, err := New(client, Options{StoreName: storeName, TTL: shortTTL})
	require.NoError(t, err)
	got, err := collect(t, fresh, session, "1", 0)
	require.NoError(t, err, "the stream record outlives its events")
	assert.Empty(t, got)
	require.NoError(t, fresh.Append(ctx, session, "1", []byte("b")))
	got, err = collect(t, fresh, session, "1", 0)
	require.NoError(t, err)
	assert.Equal(t, []string{"b"}, got, "the stream keeps its numbering")
}
