package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dapr/dapr-mcp-server/test/daprit"
)

// These tests run against a real sidecar and state store; see daprit.StoreEnv.

func TestHTTPResumeWithDaprStateStore(t *testing.T) {
	client, storeName := daprit.StateClient(t)
	resumeAfterDroppedStream(t, client, storeName)
}

func TestHTTPRejectsAMissingDaprStateStore(t *testing.T) {
	client, _ := daprit.StateClient(t)
	cfg := resolveFlags(t, "--event-store", "nosuchstore")
	_, err := cfg.streamableOptions(t.Context(), client, discardLogger())
	require.ErrorContains(t, err, "is not usable")
}
