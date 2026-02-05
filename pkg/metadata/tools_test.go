package metadata

import (
	"context"
	"errors"
	"testing"

	dapr "github.com/dapr/go-sdk/client"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	"github.com/dapr/dapr-mcp-server/test/mocks"
)

func TestGetMetadataTool(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*mocks.MockDaprClient)
		wantErr       bool
		wantContent   string
		expectedCount int
		nilClient     bool
	}{
		{
			name: "successful metadata retrieval with multiple components",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{
						{Name: "statestore", Type: "state.redis", Version: "v1", Capabilities: []string{"etag", "transaction"}},
						{Name: "pubsub", Type: "pubsub.redis", Version: "v1", Capabilities: []string{}},
						{Name: "secretstore", Type: "secretstores.kubernetes", Version: "v1", Capabilities: nil},
					},
				}, nil)
			},
			wantErr:       false,
			wantContent:   "Successfully retrieved 3 Dapr component(s)",
			expectedCount: 3,
		},
		{
			name: "successful metadata retrieval with no matching components",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{
						{Name: "other", Type: "middleware.http", Version: "v1", Capabilities: []string{}},
					},
				}, nil)
			},
			wantErr:       false,
			wantContent:   "Successfully retrieved 0 Dapr component(s)",
			expectedCount: 0,
		},
		{
			name: "successful metadata retrieval with all component types",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{
						{Name: "statestore", Type: "state.redis", Version: "v1", Capabilities: []string{}},
						{Name: "pubsub", Type: "pubsub.kafka", Version: "v1", Capabilities: []string{}},
						{Name: "inputbinding", Type: "bindings.cron", Version: "v1", Capabilities: []string{}},
						{Name: "conversation", Type: "conversation.openai", Version: "v1", Capabilities: []string{}},
						{Name: "secretstore", Type: "secretstores.vault", Version: "v1", Capabilities: []string{}},
						{Name: "lockstore", Type: "lock.redis", Version: "v1", Capabilities: []string{}},
						{Name: "cryptostore", Type: "crypto.azure", Version: "v1", Capabilities: []string{}},
					},
				}, nil)
			},
			wantErr:       false,
			wantContent:   "Successfully retrieved 7 Dapr component(s)",
			expectedCount: 7,
		},
		{
			name: "metadata retrieval failure",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(nil, errors.New("connection refused"))
			},
			wantErr:     true,
			wantContent: "Error fetching live Dapr component list",
		},
		{
			name: "metadata retrieval with nil capabilities",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{
						{Name: "statestore", Type: "state.memory", Version: "v1", Capabilities: nil},
					},
				}, nil)
			},
			wantErr:       false,
			wantContent:   "Successfully retrieved 1 Dapr component(s)",
			expectedCount: 1,
		},
		{
			name:        "nil client",
			setupMock:   func(m *mocks.MockDaprClient) {},
			nilClient:   true,
			wantErr:     true,
			wantContent: "Dapr client not initialized",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.nilClient {
				metadataClient = nil
			} else {
				mockClient := new(mocks.MockDaprClient)
				tt.setupMock(mockClient)
				metadataClient = mockClient
			}

			result, wrapper, err := getMetadataTool(context.Background(), &mcp.CallToolRequest{}, nil)

			assert.NoError(t, err)
			assert.Equal(t, tt.wantErr, result.IsError)
			if len(result.Content) > 0 {
				textContent, ok := result.Content[0].(*mcp.TextContent)
				assert.True(t, ok)
				assert.Contains(t, textContent.Text, tt.wantContent)
			}

			if !tt.wantErr && !tt.nilClient {
				assert.Equal(t, tt.expectedCount, len(wrapper.Components))
			}
		})
	}
}

func TestGetLiveComponentList(t *testing.T) {
	tests := []struct {
		name          string
		setupMock     func(*mocks.MockDaprClient)
		wantErr       bool
		expectedCount int
		expectedTypes []string
	}{
		{
			name: "filter only relevant component types",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{
						{Name: "statestore", Type: "state.redis", Version: "v1"},
						{Name: "pubsub", Type: "pubsub.redis", Version: "v1"},
						{Name: "middleware", Type: "middleware.http.ratelimit", Version: "v1"},
						{Name: "nameresolver", Type: "nameresolution.consul", Version: "v1"},
					},
				}, nil)
			},
			wantErr:       false,
			expectedCount: 2,
			expectedTypes: []string{"state.redis", "pubsub.redis"},
		},
		{
			name: "empty components list",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
					RegisteredComponents: []*dapr.MetadataRegisteredComponents{},
				}, nil)
			},
			wantErr:       false,
			expectedCount: 0,
		},
		{
			name: "API error",
			setupMock: func(m *mocks.MockDaprClient) {
				m.On("GetMetadata", mock.Anything).Return(nil, errors.New("sidecar not ready"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockClient := new(mocks.MockDaprClient)
			tt.setupMock(mockClient)

			components, err := GetLiveComponentList(context.Background(), mockClient)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedCount, len(components))

				if tt.expectedTypes != nil {
					for i, expectedType := range tt.expectedTypes {
						assert.Equal(t, expectedType, components[i].Type)
					}
				}
			}

			mockClient.AssertExpectations(t)
		})
	}
}

func TestRegisterTools(t *testing.T) {
	mockClient := new(mocks.MockDaprClient)
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "v1.0.0"}, nil)

	// Should not panic
	RegisterTools(server, mockClient, nil)

	assert.Equal(t, mockClient, metadataClient)
}

// mockMetadataClient implements MetadataClient for testing
type mockMetadataClient struct {
	mock.Mock
}

func (m *mockMetadataClient) GetMetadata(ctx context.Context) (*dapr.GetMetadataResponse, error) {
	args := m.Called(ctx)
	if args.Get(0) == nil {
		return nil, args.Error(1)
	}
	return args.Get(0).(*dapr.GetMetadataResponse), args.Error(1)
}

func TestGetLiveComponentListWithInterfaceMock(t *testing.T) {
	mockMeta := new(mockMetadataClient)
	mockMeta.On("GetMetadata", mock.Anything).Return(&dapr.GetMetadataResponse{
		RegisteredComponents: []*dapr.MetadataRegisteredComponents{
			{Name: "test-state", Type: "state.test", Version: "v1", Capabilities: []string{"etag"}},
		},
	}, nil)

	components, err := GetLiveComponentList(context.Background(), mockMeta)

	assert.NoError(t, err)
	assert.Len(t, components, 1)
	assert.Equal(t, "test-state", components[0].Name)
	assert.Equal(t, "state.test", components[0].Type)
	assert.Equal(t, "v1", components[0].Version)
	assert.Contains(t, components[0].Capabilities, "etag")

	mockMeta.AssertExpectations(t)
}

func TestComponentInfoStruct(t *testing.T) {
	component := ComponentInfo{
		Name:         "test-component",
		Type:         "state.redis",
		Version:      "v1",
		Capabilities: []string{"etag", "transaction"},
	}

	assert.Equal(t, "test-component", component.Name)
	assert.Equal(t, "state.redis", component.Type)
	assert.Equal(t, "v1", component.Version)
	assert.Len(t, component.Capabilities, 2)
}

func TestComponentListWrapper(t *testing.T) {
	wrapper := ComponentListWrapper{
		Components: []ComponentInfo{
			{Name: "comp1", Type: "state.redis", Version: "v1", Capabilities: []string{}},
			{Name: "comp2", Type: "pubsub.kafka", Version: "v1", Capabilities: []string{}},
		},
	}

	assert.Len(t, wrapper.Components, 2)
	assert.Equal(t, "comp1", wrapper.Components[0].Name)
	assert.Equal(t, "comp2", wrapper.Components[1].Name)
}
