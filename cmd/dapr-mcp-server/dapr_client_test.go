package main

import (
	"context"
	"errors"
	"testing"
	"time"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testRetryDelay = time.Millisecond
	longRetryDelay = time.Hour
)

var errSidecarDown = errors.New("sidecar down")

// flakyFactory fails failures times before returning client, counting every call.
func flakyFactory(failures int, client dapr.Client, calls *int) daprClientFactory {
	return func() (dapr.Client, error) {
		*calls++
		if *calls <= failures {
			return nil, errSidecarDown
		}
		return client, nil
	}
}

func TestInitializeDaprClient(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		failures  int
		wantCalls int
		wantErr   error
	}{
		{name: "first attempt succeeds", failures: 0, wantCalls: 1},
		{name: "succeeds on last attempt", failures: daprClientMaxAttempts - 1, wantCalls: daprClientMaxAttempts},
		{name: "retries exhausted", failures: daprClientMaxAttempts, wantCalls: daprClientMaxAttempts, wantErr: errSidecarDown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			want := newTestDaprClient()
			calls := 0

			got, err := initializeDaprClient(context.Background(), flakyFactory(tt.failures, want, &calls), testRetryDelay, discardLogger())

			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Same(t, want, got)
		})
	}
}

func TestInitializeDaprClientContextCanceled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0

	got, err := initializeDaprClient(ctx, flakyFactory(daprClientMaxAttempts, nil, &calls), longRetryDelay, discardLogger())

	require.ErrorIs(t, err, context.Canceled)
	assert.Nil(t, got)
	assert.Equal(t, 1, calls, "no retry after the context is canceled")
}
