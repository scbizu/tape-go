// Package runner defines the runtime-neutral model turn boundary.
package runner

import (
	"context"
	"encoding/json"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

type Message struct {
	Role        Role
	Text        string
	ToolCalls   []ToolCall
	ToolResults []ToolResult
}

type ToolSpec struct {
	Name        string
	Description string
	InputSchema json.RawMessage
}

type ToolCall struct {
	ID        string
	Name      string
	Arguments json.RawMessage
}

type ToolResult struct {
	ID     string
	Name   string
	Result json.RawMessage
}

type Request struct {
	Model             string
	SystemInstruction string
	Messages          []Message
	Tools             []ToolSpec
}

type Response struct {
	Output    string
	ToolCalls []ToolCall
}

type Runner interface {
	RunTurn(context.Context, Request) (Response, error)
}
