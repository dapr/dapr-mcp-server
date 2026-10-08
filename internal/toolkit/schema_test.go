package toolkit

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type schemaTestArgs struct {
	Name        string            `json:"name"`
	Items       []string          `json:"items"`
	Temperature *float64          `json:"temperature,omitempty"`
	Metadata    map[string]string `json:"metadata,omitempty"`
}

func TestInputSchemaDropsNullTypes(t *testing.T) {
	t.Parallel()

	schema := InputSchema[schemaTestArgs]()

	tests := []struct {
		prop string
		want string
	}{
		{prop: "name", want: "string"},
		{prop: "items", want: "array"},
		{prop: "temperature", want: "number"},
		{prop: "metadata", want: "object"},
	}
	for _, tt := range tests {
		t.Run(tt.prop, func(t *testing.T) {
			t.Parallel()
			prop := schema.Properties[tt.prop]
			require.NotNil(t, prop)
			assert.Equal(t, tt.want, prop.Type)
			assert.Empty(t, prop.Types)
		})
	}
	assert.Equal(t, []string{"name", "items"}, schema.Required)
}

func TestDropNullTypeKeepsOtherUnions(t *testing.T) {
	t.Parallel()

	schema := InputSchema[schemaTestArgs]()
	schema.Properties["name"].Types = []string{"null", "string", "integer"}
	schema.Properties["name"].Type = ""

	dropNullType(schema.Properties["name"])

	assert.Equal(t, []string{"string", "integer"}, schema.Properties["name"].Types)
}

type nestedItem struct {
	Tags []string `json:"tags"`
}

type nestedArgs struct {
	Items []nestedItem `json:"items"`
}

func TestInputSchemaDropsNestedNullTypes(t *testing.T) {
	t.Parallel()

	items := InputSchema[nestedArgs]().Properties["items"]
	require.NotNil(t, items.Items)
	tags := items.Items.Properties["tags"]
	require.NotNil(t, tags)
	assert.Equal(t, "array", tags.Type)
	assert.Empty(t, tags.Types)
}
