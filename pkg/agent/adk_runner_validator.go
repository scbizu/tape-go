package agent

import (
	"errors"
	"fmt"

	"google.golang.org/adk/model"
	"google.golang.org/genai"

	taperunner "github.com/scbizu/tape-go/pkg/runner"
)

type turnValidator struct{}

func (turnValidator) validateRequest(request taperunner.Request) error {
	if len(request.Messages) == 0 {
		return errors.New("runner request has no messages")
	}
	for i, message := range request.Messages {
		if err := validateMessage(message); err != nil {
			return fmt.Errorf("runner message %d: %w", i, err)
		}
	}

	seen := make(map[string]struct{}, len(request.Tools))
	for i, spec := range request.Tools {
		if spec.Name == "" {
			return fmt.Errorf("runner tool %d has empty name", i)
		}
		if _, ok := seen[spec.Name]; ok {
			return fmt.Errorf("runner tool %q is duplicated", spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
	return nil
}

func validateMessage(message taperunner.Message) error {
	switch message.Role {
	case taperunner.RoleUser:
		return validateUserMessage(message)
	case taperunner.RoleAssistant:
		return validateAssistantMessage(message)
	case taperunner.RoleTool:
		return validateToolMessage(message)
	default:
		return fmt.Errorf("unsupported role %q", message.Role)
	}
}

func validateUserMessage(message taperunner.Message) error {
	if message.Text == "" || len(message.ToolCalls) > 0 || len(message.ToolResults) > 0 {
		return errors.New("user message must contain only text")
	}
	return nil
}

func validateAssistantMessage(message taperunner.Message) error {
	if len(message.ToolResults) > 0 {
		return errors.New("assistant message must contain exactly one of text or tool calls")
	}
	if message.Text == "" && len(message.ToolCalls) == 0 {
		return errors.New("assistant message must contain exactly one of text or tool calls")
	}
	if message.Text != "" && len(message.ToolCalls) > 0 {
		return errors.New("assistant message must contain exactly one of text or tool calls")
	}
	for i, call := range message.ToolCalls {
		if call.Name == "" {
			return fmt.Errorf("tool call %d has empty name", i)
		}
	}
	return nil
}

func validateToolMessage(message taperunner.Message) error {
	if message.Text != "" || len(message.ToolCalls) > 0 || len(message.ToolResults) == 0 {
		return errors.New("tool message must contain only tool results")
	}
	for i, result := range message.ToolResults {
		if result.Name == "" {
			return fmt.Errorf("tool result %d has empty name", i)
		}
	}
	return nil
}

func (turnValidator) validateModelResponse(response *model.LLMResponse) error {
	if response == nil {
		return errors.New("ADK model returned nil response")
	}
	if response.ErrorCode != "" || response.ErrorMessage != "" {
		return fmt.Errorf("ADK model response error %q: %s", response.ErrorCode, response.ErrorMessage)
	}
	if response.Partial {
		return errors.New("ADK model returned partial response for non-streaming turn")
	}
	if response.Content == nil {
		return errors.New("ADK model response has no content")
	}
	if response.Content.Role != "" && response.Content.Role != genai.RoleModel {
		return fmt.Errorf("ADK model response has role %q", response.Content.Role)
	}
	for i, part := range response.Content.Parts {
		if part == nil {
			return fmt.Errorf("ADK model response part %d is nil", i)
		}
		switch {
		case part.Text != "" && part.FunctionCall == nil:
		case part.FunctionCall != nil && part.Text == "":
			if part.FunctionCall.Name == "" {
				return fmt.Errorf("ADK tool call %d has empty name", i)
			}
		default:
			return fmt.Errorf("ADK model response part %d is unsupported or conflicting", i)
		}
	}
	return nil
}

func (turnValidator) validateResponse(response taperunner.Response) error {
	if response.Output == "" && len(response.ToolCalls) == 0 {
		return errors.New("ADK model response is empty")
	}
	if response.Output != "" && len(response.ToolCalls) > 0 {
		return errors.New("ADK model response mixes final output and tool calls")
	}
	return nil
}
