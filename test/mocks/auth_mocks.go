// Package mocks provides mock implementations for testing.
package mocks

import (
	"context"

	"github.com/stretchr/testify/mock"

	"github.com/dapr/dapr-mcp-server/pkg/auth"
)

// MockAuthenticator is a mock implementation of the Authenticator interface.
type MockAuthenticator struct {
	mock.Mock
}

// Authenticate mocks the Authenticate method.
func (m *MockAuthenticator) Authenticate(ctx context.Context, token string) (*auth.Identity, error) {
	args := m.Called(ctx, token)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*auth.Identity), args.Error(1)
}

// Mode mocks the Mode method.
// Without an expectation for Mode it returns an empty mode instead of panicking,
// because callers such as the auth middleware log it on every failure.
func (m *MockAuthenticator) Mode() auth.AuthMode {
	if !m.hasExpectation("Mode") {
		return ""
	}
	mode, _ := m.Called().Get(0).(auth.AuthMode)
	return mode
}

func (m *MockAuthenticator) hasExpectation(method string) bool {
	for _, call := range m.ExpectedCalls {
		if call.Method == method {
			return true
		}
	}
	return false
}

// Ensure MockAuthenticator implements auth.Authenticator
var _ auth.Authenticator = (*MockAuthenticator)(nil)
