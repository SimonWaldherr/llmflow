package llm

// agent.go defines the AgentGenerator interface used by the agentic loop when
// tool calling is enabled.  Not all LLM clients implement this interface; the
// app falls back to the standard Generator for clients that don't.

import "context"

// Message is a single turn in a multi-turn conversation.
type Message struct {
	// Role is one of "system", "user", "assistant", or "tool".
	Role string
	// Content is the text content of the message.
	Content string
	// ToolCalls is non-empty when Role=="assistant" and the model has requested
	// one or more tool invocations.
	ToolCalls []ToolCall
	// ToolCallID is set when Role=="tool" (identifies the call being answered).
	ToolCallID string
	// ToolName is the name of the tool that produced this result (Role=="tool").
	ToolName string
}

// ToolCall describes a single tool invocation requested by the LLM.
type ToolCall struct {
	// ID is an opaque identifier assigned by the LLM (must be echoed back).
	ID string
	// Name is the tool name (matches Tool.Name).
	Name string
	// Args is the JSON-encoded argument object.
	Args string
}

// ToolDef describes a tool the LLM may call.
type ToolDef struct {
	Name        string
	Description string
	// Parameters is a JSON Schema object (any JSON-serializable value).
	Parameters any
}

// AgentRequest is the input to a single tool-aware generation step.
type AgentRequest struct {
	Messages []Message
	Tools    []ToolDef
}

// AgentResponse is the result of a single tool-aware generation step.
type AgentResponse struct {
	// Content holds the final text response (non-empty only when ToolCalls is
	// empty, i.e., the model has finished).
	Content string
	// ToolCalls is non-empty when the model wants to call one or more tools
	// before giving a final answer.
	ToolCalls []ToolCall
}

// AgentGenerator extends Generator with multi-turn tool-calling support.
type AgentGenerator interface {
	Generator
	// GenerateAgent performs one step of the agentic loop.  It sends the full
	// conversation (with tool definitions) to the LLM and returns either a
	// final text answer or a set of tool calls to execute.
	GenerateAgent(ctx context.Context, req AgentRequest) (*AgentResponse, error)
}
