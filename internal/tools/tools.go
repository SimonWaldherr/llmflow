// Package tools defines the Tool interface and built-in tools that an LLM
// agent can call during an agentic processing loop.
package tools

import "context"

// Tool describes a callable tool available to an LLM agent.
type Tool struct {
	// Name is the machine-readable identifier (snake_case).
	Name string
	// Description explains what the tool does (shown to the LLM).
	Description string
	// Parameters is a JSON Schema object (as a raw byte slice) describing the
	// tool's input parameters.
	Parameters []byte
	// Execute runs the tool with JSON-encoded arguments and returns a string
	// result that will be fed back to the LLM.
	Execute func(ctx context.Context, argsJSON string) (string, error)
}
