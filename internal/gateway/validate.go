package gateway

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// CompileSchema compiles a JSON schema for reuse across multiple validation calls.
// Called once per CompleteRequest at driver construction or on first use.
func CompileSchema(rawSchema json.RawMessage) (*jsonschema.Schema, error) {
	// v6 requires the schema to be pre-parsed via UnmarshalJSON before AddResource.
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(rawSchema))
	if err != nil {
		return nil, fmt.Errorf("gateway: parse schema JSON: %w", err)
	}

	c := jsonschema.NewCompiler()
	const id = "urn:stowage:response-schema"
	if err := c.AddResource(id, doc); err != nil {
		return nil, fmt.Errorf("gateway: add schema resource: %w", err)
	}
	sch, err := c.Compile(id)
	if err != nil {
		return nil, fmt.Errorf("gateway: compile schema: %w", err)
	}
	return sch, nil
}

// ValidateJSON validates a JSON byte slice against a pre-compiled schema.
// Returns nil on success; returns a wrapped ErrSchemaValidation on failure.
func ValidateJSON(sch *jsonschema.Schema, data json.RawMessage) error {
	v, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("%w: unmarshal model output: %w", ErrSchemaValidation, err)
	}
	if err := sch.Validate(v); err != nil {
		return fmt.Errorf("%w: %w", ErrSchemaValidation, err)
	}
	return nil
}
