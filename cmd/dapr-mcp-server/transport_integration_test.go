package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/pkg/eventstore"
	"github.com/dapr/dapr-mcp-server/test/mocks"
)

// These tests start the real HTTP handler in-process and drive it with the go-sdk client.
// They use t.Setenv (through clearAuthEnv), so they cannot run in parallel.

const (
	integrationTimeout = 15 * time.Second
	pingToolName       = "integration_ping"
	stepsToolName      = "integration_steps"
	protocolA          = "2025-11-25"
	protocolB          = "2026-07-28"
	lastStepMessage    = "step 2"
)

type pingArgs struct{}

type pingOut struct {
	Pong bool `json:"pong"`
}

func addPingTool(server *mcp.Server) {
	mcp.AddTool(server, &mcp.Tool{Name: pingToolName, Description: "answers pong"},
		func(context.Context, *mcp.CallToolRequest, pingArgs) (*mcp.CallToolResult, pingOut, error) {
			return nil, pingOut{Pong: true}, nil
		})
}

// startHTTPServer serves the production handler built from cfg over a test listener.
// It returns the endpoint and, when cfg names an event store, a counter of its replays.
// With an event store, the test also fails if the SDK opens a stream the store would take
// for an existing one; see countingStore.Open.
func startHTTPServer(t *testing.T, cfg transportConfig, state eventstore.StateClient, setup func(*mcp.Server)) (string, *countingStore) {
	t.Helper()
	clearAuthEnv(t)
	server := newMCPServer("integration", cfg.protocolVersions)
	addPingTool(server)
	if setup != nil {
		setup(server)
	}
	opts, err := cfg.streamableOptions(t.Context(), state, discardLogger())
	require.NoError(t, err)
	var counter *countingStore
	if opts.EventStore != nil {
		counter = &countingStore{EventStore: opts.EventStore}
		opts.EventStore = counter
		// Registered before the server's cleanup, so it runs after the server has stopped.
		t.Cleanup(func() {
			assert.Empty(t, counter.unexpectedOpens(), "stream IDs the event store would read as existing streams")
		})
	}
	handler, _, err := buildHTTPHandler(t.Context(), server, opts, newReadyHealthHandler(), nil, discardLogger())
	require.NoError(t, err)
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv.URL, counter
}

func connect(t *testing.T, endpoint, version string, client *http.Client, opts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
	defer cancel()
	c := mcp.NewClient(&mcp.Implementation{Name: "integration", Version: "test"}, opts)
	session, err := c.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: client, DisableStandaloneSSE: true},
		&mcp.ClientSessionOptions{ProtocolVersion: version})
	require.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func assertListsPing(t *testing.T, session *mcp.ClientSession) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
	defer cancel()
	res, err := session.ListTools(ctx, nil)
	require.NoError(t, err)
	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
	}
	assert.Contains(t, names, pingToolName)
}

func resolveFlags(t *testing.T, args ...string) transportConfig {
	t.Helper()
	cfg, err := parseTransportFlags(t, args...).resolve(true)
	require.NoError(t, err)
	return cfg
}

func TestHTTPSessionMode(t *testing.T) {
	endpoint, _ := startHTTPServer(t, resolveFlags(t), nil, nil)
	// A newest-version client tries server/discover first and falls back to initialize,
	// because the stateful transport serves only session-era protocols.
	session := connect(t, endpoint, "", nil, nil)

	assert.Equal(t, protocolA, session.InitializeResult().ProtocolVersion)
	assert.NotEmpty(t, session.ID(), "session mode issues an Mcp-Session-Id")
	assertListsPing(t, session)
}

func TestHTTPStatelessMode(t *testing.T) {
	endpoint, _ := startHTTPServer(t, resolveFlags(t, "--stateless"), nil, nil)

	t.Run("newest protocol uses server/discover", func(t *testing.T) {
		session := connect(t, endpoint, "", nil, nil)
		assert.Equal(t, protocolB, session.InitializeResult().ProtocolVersion)
		assert.Empty(t, session.ID(), "stateless mode issues no session id")
		assertListsPing(t, session)
	})
	t.Run("session-era client still works", func(t *testing.T) {
		session := connect(t, endpoint, protocolA, nil, nil)
		assert.Equal(t, protocolA, session.InitializeResult().ProtocolVersion)
		assert.Empty(t, session.ID())
		assertListsPing(t, session)
	})
}

func TestHTTPSpecVersionPin(t *testing.T) {
	endpoint, _ := startHTTPServer(t, resolveFlags(t, "--stateless", "--spec-version", protocolA), nil, nil)
	// The server refuses server/discover at the excluded version, so the client falls back.
	session := connect(t, endpoint, "", nil, nil)

	assert.Equal(t, protocolA, session.InitializeResult().ProtocolVersion)
	assertListsPing(t, session)
}

// TestHTTPResumeAfterDroppedStream cuts the tools/call response stream after the first progress
// notification and checks that the client gets the rest by resuming from the Dapr-backed event store.
func TestHTTPResumeAfterDroppedStream(t *testing.T) {
	state := mocks.NewFakeStateStore()
	session := resumeAfterDroppedStream(t, state, "statestore")
	assert.NotEmpty(t, state.Keys("statestore"), "events went through the Dapr state store")

	require.NoError(t, session.Close())
	assert.Eventually(t, func() bool { return len(state.Keys("statestore")) == 0 }, integrationTimeout, 10*time.Millisecond,
		"closing the session deletes its events; left: %v", state.Keys("statestore"))
}

// resumeAfterDroppedStream runs the dropped-stream scenario with an event store that writes
// through state to storeName, and returns the client session, still open.
func resumeAfterDroppedStream(t *testing.T, state eventstore.StateClient, storeName string) *mcp.ClientSession {
	t.Helper()
	// The client's resume waits until the tool has sent its second step, so that step can only
	// reach the client by a replay from the event store, not live on the resumed stream.
	lastStepSent := make(chan struct{})
	dropper := newStreamDropper(lastStepSent)
	lastStepErr := make(chan error, 1)
	cfg := resolveFlags(t, "--event-store", storeName)
	endpoint, replays := startHTTPServer(t, cfg, state, func(s *mcp.Server) {
		addStepsTool(s, dropper.dropped, lastStepErr, lastStepSent)
	})

	var mu sync.Mutex
	var progress []string
	// The client runs progress handlers asynchronously, so CallTool can return before the
	// handler for the replayed last step has run. The test waits for it explicitly.
	lastStep := make(chan struct{})
	var lastStepOnce sync.Once
	session := connect(t, endpoint, protocolA, &http.Client{Transport: dropper}, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			mu.Lock()
			defer mu.Unlock()
			progress = append(progress, req.Params.Message)
			if req.Params.Message == lastStepMessage {
				lastStepOnce.Do(func() { close(lastStep) })
			}
		},
	})

	ctx, cancel := context.WithTimeout(t.Context(), integrationTimeout)
	defer cancel()
	params := &mcp.CallToolParams{Name: stepsToolName, Arguments: map[string]any{}}
	params.SetProgressToken("resume-test")
	res, err := session.CallTool(ctx, params)
	require.NoError(t, err)
	require.False(t, res.IsError)

	select {
	case <-lastStep:
	case <-ctx.Done():
		t.Fatalf("the replayed %q notification never reached the client: %v", lastStepMessage, ctx.Err())
	}

	select {
	case err := <-lastStepErr:
		require.NoError(t, err, "the server must record the %q notification even though its stream is gone", lastStepMessage)
	case <-ctx.Done():
		t.Fatalf("the tool never sent %q: %v", lastStepMessage, ctx.Err())
	}

	assert.Equal(t, int32(1), dropper.drops.Load(), "the stream must have been cut once")
	assert.GreaterOrEqual(t, dropper.resumes.Load(), int32(1), "the client must have resumed with Last-Event-ID")
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"step 1", lastStepMessage}, progress, "no notification lost across the drop")
	assert.GreaterOrEqual(t, replays.afters.Load(), int32(1), "the server must have replayed from the event store")
	assert.True(t, replays.replayedContaining(lastStepMessage), "the %q notification must come from the event store", lastStepMessage)
	return session
}

type stepsArgs struct{}

// addStepsTool registers a tool that reports one step, waits until the test has cut
// the stream, then reports a second step and returns. The result of sending the second
// step goes to lastStepErr, which must have room for one value, and lastStepSent is closed after.
func addStepsTool(server *mcp.Server, dropped <-chan struct{}, lastStepErr chan<- error, lastStepSent chan<- struct{}) {
	mcp.AddTool(server, &mcp.Tool{Name: stepsToolName, Description: "two progress steps around a dropped stream"},
		func(ctx context.Context, req *mcp.CallToolRequest, _ stepsArgs) (*mcp.CallToolResult, any, error) {
			token := req.Params.GetProgressToken()
			notify := func(msg string, n float64) error {
				return req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Message: msg, Progress: n, Total: 2})
			}
			if err := notify("step 1", 1); err != nil {
				return nil, nil, err
			}
			select {
			case <-dropped:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			// The client's connection is gone. The SDK still records the event in the event store
			// for replay, and reports success because the store accepted it.
			lastStepErr <- notify(lastStepMessage, 2)
			close(lastStepSent)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "done"}}}, nil, nil
		})
}

// streamDropper is an HTTP transport that cuts the first tools/call response stream
// right after its first progress notification, the way a network drop would. It holds
// every resume (a request with Last-Event-ID) until resumeAfter is closed.
type streamDropper struct {
	base        http.RoundTripper
	once        sync.Once
	dropped     chan struct{}
	resumeAfter <-chan struct{}
	drops       atomic.Int32
	resumes     atomic.Int32
}

func newStreamDropper(resumeAfter <-chan struct{}) *streamDropper {
	return &streamDropper{base: http.DefaultTransport, dropped: make(chan struct{}), resumeAfter: resumeAfter}
}

func (d *streamDropper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Header.Get("Last-Event-ID") != "" {
		d.resumes.Add(1)
		select {
		case <-d.resumeAfter:
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}
	}
	isCall, err := isToolCall(req)
	if err != nil {
		return nil, err
	}
	resp, err := d.base.RoundTrip(req)
	if err != nil || !isCall || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		return resp, err
	}
	cut := false
	d.once.Do(func() { cut = true })
	if cut {
		resp.Body = &cutAfterProgress{body: resp.Body, reader: bufio.NewReader(resp.Body), onCut: d.cut}
	}
	return resp, nil
}

func (d *streamDropper) cut() {
	d.drops.Add(1)
	close(d.dropped)
}

// isToolCall reports whether req posts a tools/call, restoring the body it reads.
func isToolCall(req *http.Request) (bool, error) {
	if req.Method != http.MethodPost || req.Body == nil {
		return false, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return false, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return bytes.Contains(body, []byte(`"tools/call"`)), nil
}

// cutAfterProgress passes SSE lines through until the first progress event has been delivered,
// then closes the connection and reports an unexpected EOF.
type cutAfterProgress struct {
	body     io.ReadCloser
	reader   *bufio.Reader
	pending  []byte
	sawEvent bool
	done     bool
	onCut    func()
}

func (c *cutAfterProgress) Read(p []byte) (int, error) {
	for len(c.pending) == 0 {
		if c.done {
			return 0, io.ErrUnexpectedEOF
		}
		line, err := c.reader.ReadBytes('\n')
		if err != nil {
			return 0, err
		}
		c.pending = line
		if bytes.Contains(line, []byte("notifications/progress")) {
			c.sawEvent = true
		}
		if c.sawEvent && len(bytes.TrimSpace(line)) == 0 {
			// The progress event is complete; drop the connection after delivering it.
			c.done = true
			_ = c.body.Close()
			c.onCut()
		}
	}
	n := copy(p, c.pending)
	c.pending = c.pending[n:]
	return n, nil
}

func (c *cutAfterProgress) Close() error {
	if c.done {
		return nil
	}
	return c.body.Close()
}

// countingStore records how often the server replays from the event store and what it
// replays, and checks the stream IDs the SDK opens.
type countingStore struct {
	mcp.EventStore
	afters atomic.Int32

	mu       sync.Mutex
	replayed [][]byte
	// unexpected holds the stream IDs passed to Open that are neither the standalone stream
	// nor shaped like a stream the SDK has just created.
	unexpected []string
}

// Open checks the assumption behind the store's shortcut for new streams: the SDK opens only
// the standalone stream "" and streams it has just named with crypto/rand.Text. An SDK that
// reopened an existing stream would otherwise have its event count reset without a failure.
func (c *countingStore) Open(ctx context.Context, sessionID, streamID string) error {
	if streamID != "" && !eventstore.IsFreshStreamID(streamID) {
		c.mu.Lock()
		c.unexpected = append(c.unexpected, streamID)
		c.mu.Unlock()
	}
	return c.EventStore.Open(ctx, sessionID, streamID)
}

func (c *countingStore) After(ctx context.Context, sessionID, streamID string, index int) iter.Seq2[[]byte, error] {
	c.afters.Add(1)
	return func(yield func([]byte, error) bool) {
		for data, err := range c.EventStore.After(ctx, sessionID, streamID, index) {
			if err == nil {
				c.mu.Lock()
				c.replayed = append(c.replayed, bytes.Clone(data))
				c.mu.Unlock()
			}
			if !yield(data, err) {
				return
			}
		}
	}
}

// replayedContaining reports whether any replayed event contains text.
func (c *countingStore) replayedContaining(text string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, data := range c.replayed {
		if bytes.Contains(data, []byte(text)) {
			return true
		}
	}
	return false
}

func (c *countingStore) unexpectedOpens() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.unexpected)
}
