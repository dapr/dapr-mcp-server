package toolkit

import (
	"fmt"
	"slices"

	"github.com/google/jsonschema-go/jsonschema"
)

const typeNull = "null"

// InputSchema infers the input schema of a tool whose arguments are of type T,
// with "null" removed from the types of its properties, including nested ones.
// Inference types slices, maps and pointers as ["null", X], which some LLM clients reject.
// The tools express an optional argument by omitting it, never by sending null,
// so an argument sent as null fails input validation.
// It panics if T cannot be reflected into a schema, which is a programming error
// surfaced when the tool is registered at startup.
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
	if schema == nil {
		return
	}
	if slices.Contains(schema.Types, typeNull) {
		types := slices.DeleteFunc(slices.Clone(schema.Types), func(t string) bool { return t == typeNull })
		if len(types) == 1 {
			schema.Type, schema.Types = types[0], nil
		} else {
			schema.Types = types
		}
	}
	for _, prop := range schema.Properties {
		dropNullType(prop)
	}
	dropNullType(schema.Items)
	dropNullType(schema.AdditionalProperties)
}
