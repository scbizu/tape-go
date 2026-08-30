package runner

import (
	"context"
	"encoding/json"
	"testing"
)

func TestRequestUsesRuntimeNeutralTypes(t *testing.T) {
	t.Parallel()

	request := Request{
		SystemInstruction: "be concise",
		Messages:          []Message{{Role: RoleUser, Text: "hello"}},
		Tools: []ToolSpec{{
			Name:        "search",
			Description: "search memory",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		}},
	}

	runner := stubRunner{response: Response{Output: "done"}}
	response, err := runner.RunTurn(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Output != "done" || len(response.ToolCalls) != 0 {
		t.Fatalf("response mismatch: %#v", response)
	}
}

type stubRunner struct {
	response Response
}

func (r stubRunner) RunTurn(context.Context, Request) (Response, error) {
	return r.response, nil
}

var _ Runner = stubRunner{}
