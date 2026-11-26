// Package daprit connects opt-in integration tests to a real Dapr sidecar.
package daprit

import (
	"os"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/require"
)

// StoreEnv names the Dapr state store the integration tests write to. They run only when it
// is set, under a sidecar that loads that store (for example the Redis store from `dapr init`):
//
//	DAPR_EVENT_STORE_IT=statestore dapr run --app-id eventstore-it -- go test -run Dapr ./...
const StoreEnv = "DAPR_EVENT_STORE_IT"

// StateClient returns a new client for the sidecar and the state store to use, or skips t
// when StoreEnv is unset. The client is closed when t ends.
func StateClient(t *testing.T) (dapr.Client, string) {
	t.Helper()
	storeName := os.Getenv(StoreEnv)
	if storeName == "" {
		t.Skipf("set %s to a Dapr state store name and run under a Dapr sidecar", StoreEnv)
	}
	port := os.Getenv("DAPR_GRPC_PORT")
	require.NotEmpty(t, port, "run under a Dapr sidecar, which sets DAPR_GRPC_PORT")
	// dapr.NewClient returns a shared client, which one test's cleanup would close for the rest.
	client, err := dapr.NewClientWithPort(port)
	require.NoError(t, err)
	t.Cleanup(client.Close)
	return client, storeName
}
