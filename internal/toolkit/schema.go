package toolkit

import (
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

const typeNull = "null"

// InputSchema infers the input schema of a tool whose arguments are of type T,
// with "null" removed from the types of its top-level properties.
// Inference types slices and pointers as ["null", X], which some LLM clients reject.
// The tools express an optional argument by omitting it, never by sending null.
func InputSchema[T any]() *jsonschema.Schema {
	schema, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("infer input schema for %T: %v", *new(T), err))
	}
	for _, prop := range schema.Properties {
		dropNullType(prop)
	}
	return schema
}

func dropNullType(schema *jsonschema.Schema) {
	if !slices.Contains(schema.Types, typeNull) {
		return
	}
	types := slices.DeleteFunc(slices.Clone(schema.Types), func(t string) bool { return t == typeNull })
	if len(types) == 1 {
		schema.Type, schema.Types = types[0], nil
		return
	}
	schema.Types = types
}
