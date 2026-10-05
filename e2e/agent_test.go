package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/cucumber/godog"
	tapeagent "github.com/scbizu/tape-go/pkg/agent"
	agenttools "github.com/scbizu/tape-go/pkg/agent/tools"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/view"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/agent/llmagent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

const archivedText = "The archived reference is entry one."

type agentState struct {
	rangeWant   view.EntryRange
	rangeGot    view.EntryRange
	sawCall     bool
	sawResponse bool
	finalText   string
}

func initializeAgentSteps(sc *godog.ScenarioContext, s *behaviorState) {
	a := &agentState{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		*a = agentState{}
		return ctx, nil
	})
	sc.Step(`^a conversation archived with the summary "([^"]*)"$`, func(summary string) error {
		var err error
		s.tape, err = s.config.Open(s.ctx, s.dir)
		if err != nil {
			return err
		}
		source, err := s.tape.Store(s.ctx, entry.NewEntry(entry.WithEntryKind(entry.EntryUser), entry.WithEntryContent(archivedText)))
		if err != nil {
			return err
		}
		commands := tapeagent.NewCommandRegistry(agenttools.NewHandoffCommand(s.tape))
		result, err := commands.Command(s.ctx, nil, tapeagent.CommandCall{Name: "handoff", Args: agenttools.HandoffArgs{Summary: summary}})
		if err != nil {
			return err
		}
		anchor, ok := result.Data.(entry.HandoffAnchor)
		if !ok {
			return fmt.Errorf("handoff returned %T", result.Data)
		}
		a.rangeWant = view.EntryRange{SeqS: source.GetID(), SeqE: source.GetID().Next()}
		if anchor.Summary != summary || anchor.SeqS != a.rangeWant.SeqS || anchor.SeqE != a.rangeWant.SeqE {
			return errors.New("handoff did not archive the conversation with its summary")
		}
		if s.tape.View.Scope.SeqS != anchor.SeqE.Next() {
			return errors.New("handoff did not start a new context window")
		}
		return nil
	})
	sc.Step(`^an agent asks to rewind the latest archived context$`, func() error {
		adapter, err := tapeagent.NewTapeAdapter(s.tape, "tape-bdd")
		if err != nil {
			return err
		}
		commands := tapeagent.NewCommandRegistry(agenttools.NewRewindCommand(s.tape))
		rewind, err := agenttools.NewRewindTool(commands)
		if err != nil {
			return err
		}
		runtime, err := tapeagent.NewRuntime(llmagent.Config{Name: "rewind_agent", Model: &rewindModel{}, Tools: []tool.Tool{rewind}, BeforeModelCallbacks: []llmagent.BeforeModelCallback{adapter.ContextWindow}}, tapeagent.WithCommandRegistry(commands))
		if err != nil {
			return err
		}
		r, err := runner.New(runner.Config{AppName: "tape-bdd", Agent: runtime.Agent, SessionService: adapter, MemoryService: adapter})
		if err != nil {
			return err
		}
		for event, err := range r.Run(s.ctx, ownerID, sessionID, genai.NewContentFromText("Rewind the latest archived context.", genai.RoleUser), adkagent.RunConfig{}) {
			if err != nil {
				return err
			}
			if event.Content == nil {
				continue
			}
			for _, p := range event.Content.Parts {
				if p.FunctionCall != nil && p.FunctionCall.Name == "rewind" {
					a.sawCall = true
				}
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "rewind" {
					if !a.sawCall {
						return errors.New("rewind result arrived before tool call")
					}
					a.sawResponse = true
					data, err := json.Marshal(p.FunctionResponse.Response)
					if err != nil {
						return err
					}
					if err := json.Unmarshal(data, &a.rangeGot); err != nil {
						return err
					}
				}
				if p.Text != "" && a.sawResponse {
					a.finalText += p.Text
				}
			}
		}
		return nil
	})
	sc.Step(`^the agent receives the archived entry range from the rewind tool$`, func() error {
		if !a.sawCall || !a.sawResponse || a.rangeGot != a.rangeWant {
			return fmt.Errorf("rewind call=%v response=%v range=%v, want %v", a.sawCall, a.sawResponse, a.rangeGot, a.rangeWant)
		}
		return nil
	})
	sc.Step(`^the agent answers after receiving the tool result$`, func() error {
		if a.finalText != "Archived context located." {
			return fmt.Errorf("final answer = %q", a.finalText)
		}
		return nil
	})
	sc.Step(`^the archived conversation is still readable$`, func() error {
		found, err := s.tape.Range(s.ctx, a.rangeGot)
		if err != nil {
			return err
		}
		if len(found.Raw) != 1 || found.Raw[0].GetSummary() != archivedText {
			return errors.New("archived conversation changed or disappeared")
		}
		return nil
	})
}

// Only the model is scripted. ADK, tool dispatch, TapeAdapter and disk storage
// execute normally, without model credentials or nondeterministic tool choice.
type rewindModel struct{ calls int }

func (*rewindModel) Name() string { return "scripted-rewind" }
func (m *rewindModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls == 1 {
			yield(&model.LLMResponse{Content: &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "rewind-1", Name: "rewind", Args: map[string]any{"max_anchors": 1}}}}}}, nil)
			return
		}
		if m.calls != 2 {
			yield(nil, errors.New("unexpected model turn"))
			return
		}
		for _, c := range req.Contents {
			for _, p := range c.Parts {
				if p.FunctionResponse != nil && p.FunctionResponse.Name == "rewind" {
					yield(&model.LLMResponse{Content: genai.NewContentFromText("Archived context located.", genai.RoleModel)}, nil)
					return
				}
			}
		}
		yield(nil, errors.New("model did not receive rewind tool result"))
	}
}
