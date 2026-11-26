package main

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/eventstore"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

const replayStore = "statestore"

// sseEvent is one server-sent event as the client receives it.
type sseEvent struct {
	id   string
	data string
}

// TestHTTPStandaloneReplayKeepsStoreIndices checks that the event IDs a client receives on the
// standalone GET stream are the indices the events are stored under, so resuming from any of
// them sends nothing twice, even after the stream's first event has expired.
func TestHTTPStandaloneReplayKeepsStoreIndices(t *testing.T) {
	state := mocks.NewFakeStateStore()
	var server *mcp.Server
	endpoint, store := startHTTPServer(t, resolveFlags(t, "--event-store", replayStore), state,
		func(s *mcp.Server) { server = s })
	session := connect(t, endpoint, protocolA, nil, nil)
	sessionID := session.ID()
	serverSession := findServerSession(t, server, sessionID)

	// With no GET attached, the SDK stores notifications unrelated to a request on the standalone stream.
	for _, msg := range []string{"n0", "n1", "n2"} {
		notifyStandalone(t, serverSession, msg)
	}
	first := eventstore.DefaultKeyPrefix + "/" + url.PathEscape(sessionID) + "//0"
	require.Contains(t, state.Keys(replayStore), first, "the first standalone event is stored")
	state.Delete(replayStore, first) // as its TTL would

	// Replaying from the oldest kept event would send n1 as event 0, n2 as event 1, and every
	// later event one index low, so a later resume would send events again.
	status, _ := openStandalone(t, endpoint, sessionID, "", 0)
	assert.Equal(t, http.StatusBadRequest, status, "a GET without Last-Event-ID cannot start after an expired event")

	status, got := openStandalone(t, endpoint, sessionID, "_0", 2)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"_1", "_2"}, ids(got))
	assertStoredAt(t, store, sessionID, got)
	assert.Contains(t, got[0].data, "n1")
	assert.Contains(t, got[1].data, "n2")

	notifyStandalone(t, serverSession, "n3")
	status, got = openStandalone(t, endpoint, sessionID, "_2", 1)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, []string{"_3"}, ids(got), "a second resume sends only what the client has not seen")
	assertStoredAt(t, store, sessionID, got)
	assert.Contains(t, got[0].data, "n3")
}

func findServerSession(t *testing.T, server *mcp.Server, sessionID string) *mcp.ServerSession {
	t.Helper()
	for ss := range server.Sessions() {
		if ss.ID() == sessionID {
			return ss
		}
	}
	t.Fatalf("no server session %q", sessionID)
	return nil
}

func notifyStandalone(t *testing.T, ss *mcp.ServerSession, msg string) {
	t.Helper()
	// A context without a request makes the SDK send on the standalone stream.
	err := ss.NotifyProgress(context.Background(), &mcp.ProgressNotificationParams{ProgressToken: "standalone", Message: msg})
	require.NoError(t, err)
}

// openStandalone opens the session's standalone GET stream, resuming after lastEventID unless it
// is empty, and reads want events before closing it. It returns the status and the events read.
func openStandalone(t *testing.T, endpoint, sessionID, lastEventID string, want int) (int, []sseEvent) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
	defer cancel()
	resp := getStandalone(ctx, t, endpoint, sessionID, lastEventID)
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, nil
	}
	events, err := readEvents(bufio.NewReader(resp.Body), want)
	require.NoError(t, err)
	return resp.StatusCode, events
}

// getStandalone sends the GET, retrying while the SDK still holds the previous GET's stream.
func getStandalone(ctx context.Context, t *testing.T, endpoint, sessionID, lastEventID string) *http.Response {
	t.Helper()
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		require.NoError(t, err)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Mcp-Session-Id", sessionID)
		req.Header.Set("Mcp-Protocol-Version", protocolA)
		if lastEventID != "" {
			req.Header.Set("Last-Event-ID", lastEventID)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		if resp.StatusCode != http.StatusConflict {
			return resp
		}
		_ = resp.Body.Close()
		select {
		case <-ctx.Done():
			t.Fatalf("the standalone stream stayed claimed: %v", ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// readEvents reads want SSE events that carry data, skipping comments.
func readEvents(r *bufio.Reader, want int) ([]sseEvent, error) {
	var events []sseEvent
	var cur sseEvent
	for len(events) < want {
		line, err := r.ReadString('\n')
		if err != nil {
			return events, fmt.Errorf("after %d events: %w", len(events), err)
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if cur.data != "" {
				events = append(events, cur)
			}
			cur = sseEvent{}
		case strings.HasPrefix(line, "id:"):
			cur.id = strings.TrimSpace(strings.TrimPrefix(line, "id:"))
		case strings.HasPrefix(line, "data:"):
			cur.data += strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		}
	}
	return events, nil
}

func ids(events []sseEvent) []string {
	out := make([]string, len(events))
	for i, e := range events {
		out[i] = e.id
	}
	return out
}

// assertStoredAt checks that each event's ID names the index the store holds its data under.
func assertStoredAt(t *testing.T, store *countingStore, sessionID string, events []sseEvent) {
	t.Helper()
	for _, e := range events {
		var index int
		_, err := fmt.Sscanf(e.id, "_%d", &index)
		require.NoError(t, err, "event ID %q", e.id)
		var stored []byte
		for data, err := range store.EventStore.After(t.Context(), sessionID, "", index-1) {
			require.NoError(t, err)
			stored = data
			break
		}
		assert.JSONEq(t, string(stored), e.data, "event %s", e.id)
	}
}
