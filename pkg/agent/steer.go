package agent

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"github.com/scbizu/tape-go/pkg/ext/steer"
	"github.com/scbizu/tape-go/pkg/tape/entry"

	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// SteerOptions controls where delivered input is retained.
type SteerOptions struct {
	// PersistToTape appends delivered messages to the Main Tape as user entries.
	// The default retains them in the model context for one invocation.
	PersistToTape bool
}

// SteerRun tracks delivery within one ADK invocation.
type SteerRun struct {
	inbox     *steer.Inbox
	options   SteerOptions
	mu        sync.RWMutex
	delivered []steerDelivery
}

type steerDelivery struct {
	message steer.Message
	after   entry.Seq // Main Tape head at the transient message's insertion point.
}

type steerRunKey struct{}

// WithSteerInbox enables steer delivery through TapeAdapter.ContextWindow.
// Use a fresh context and handle for each call to Runner.Run.
func WithSteerInbox(ctx context.Context, inbox *steer.Inbox, options SteerOptions) (context.Context, *SteerRun) {
	run := &SteerRun{inbox: inbox, options: options}
	return context.WithValue(ctx, steerRunKey{}, run), run
}

// Delivered returns IDs injected by the model callback in delivery order.
// The caller decides whether to acknowledge them after processing the run.
func (r *SteerRun) Delivered() []entry.Seq {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]entry.Seq, 0, len(r.delivered))
	for _, delivery := range r.delivered {
		ids = append(ids, delivery.message.ID)
	}
	return ids
}

func steerRunFromContext(ctx context.Context) *SteerRun {
	if ctx == nil {
		return nil
	}
	run, _ := ctx.Value(steerRunKey{}).(*SteerRun)
	if run == nil || run.inbox == nil {
		return nil
	}
	return run
}

// BeforeSteerTool skips a tool if a message is pending delivery. Register it
// alongside TapeAdapter.ContextWindow. ADK still emits the matching tool result.
func BeforeSteerTool(ctx tool.Context, _ tool.Tool, _ map[string]any) (map[string]any, error) {
	run := steerRunFromContext(ctx)
	if run == nil {
		return nil, nil
	}
	run.mu.RLock()
	defer run.mu.RUnlock()
	pending, err := run.pending(ctx)
	if err != nil || len(pending) == 0 {
		return nil, err
	}
	return map[string]any{"status": "skipped", "reason": "steer_pending"}, nil
}

// pending is called with the run lock held.
func (r *SteerRun) pending(ctx context.Context) ([]steer.Message, error) {
	messages, err := r.inbox.Pending(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(messages, func(m steer.Message) bool {
		return slices.ContainsFunc(r.delivered, func(d steerDelivery) bool { return d.message.ID == m.ID })
	}), nil
}

func (a *TapeAdapter) deliverSteer(ctx context.Context, run *SteerRun, after entry.Seq, contents []*genai.Content) ([]*genai.Content, error) {
	pending, err := run.pending(ctx)
	if err != nil {
		return nil, err
	}
	for _, message := range pending {
		if run.options.PersistToTape {
			_, err := a.Tape.Store(a.tapeContext(ctx), entry.NewEntry(
				entry.WithEntryKind(entry.EntryUser),
				entry.WithEntryOwner(a.Tape.OwnerID),
				entry.WithEntryContent(message.Text),
			))
			if err != nil {
				return nil, fmt.Errorf("agent: persist steer %s: %w", message.ID, err)
			}
		}
		contents = append(contents, steerContent(message))
		run.delivered = append(run.delivered, steerDelivery{message: message, after: after})
	}
	return contents, nil
}

func steerContent(m steer.Message) *genai.Content {
	return genai.NewContentFromText(m.Text, genai.RoleUser)
}

func hasUnmatchedFunctionCalls(contents []*genai.Content) bool {
	type functionKey struct{ id, name string }
	key := func(id, name string) functionKey {
		if id != "" {
			return functionKey{id: id}
		}
		return functionKey{name: name}
	}
	calls := make(map[functionKey]int)
	for _, content := range contents {
		for _, part := range content.Parts {
			if part == nil {
				continue
			}
			if call := part.FunctionCall; call != nil {
				calls[key(call.ID, call.Name)]++
			}
			if response := part.FunctionResponse; response != nil {
				k := key(response.ID, response.Name)
				if count := calls[k]; count <= 1 {
					delete(calls, k)
				} else {
					calls[k] = count - 1
				}
			}
		}
	}
	return len(calls) > 0
}
