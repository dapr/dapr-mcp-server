package main

import (
	"context"
	"bytes"
	"io"
	"os"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureStdout returns everything written to os.Stdout while fn runs.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	require.NoError(t, err)
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()
	require.NoError(t, w.Close())
	out, err := io.ReadAll(r)
	require.NoError(t, err)
	return string(out)
}

func TestRedirectDaprSDKLogsKeepsStdoutClean(t *testing.T) {
	// An invalid timeout makes client creation fail right after the SDK logs
	// its "initializing" line, without needing a sidecar.
	t.Setenv("DAPR_CLIENT_TIMEOUT_SECONDS", "not-a-number")
	var logs bytes.Buffer
	redirectDaprSDKLogs(&logs)
	t.Cleanup(func() { redirectDaprSDKLogs(os.Stderr) })

	stdout := captureStdout(t, func() {
		_, err := dapr.NewClientWithAddressContext(context.Background(), "127.0.0.1:1")
		require.Error(t, err)
	})

	assert.Empty(t, stdout, "stdout must carry only JSON-RPC in stdio mode")
	assert.Contains(t, logs.String(), "dapr client initializing for: 127.0.0.1:1")
}
