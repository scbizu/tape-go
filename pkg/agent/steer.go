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

const (
	steerIDExtension      = "steer:id"
	steerSessionExtension = "steer:session"
)

// SteerRun tracks delivery within one ADK invocation.
type SteerRun struct {
	inbox     *steer.Inbox
	mu        sync.RWMutex
	delivered []entry.Seq
}

type steerRunKey struct{}

// WithSteerInbox enables steer delivery through TapeAdapter.ContextWindow.
// Use a fresh context and handle for each call to Runner.Run.
func WithSteerInbox(ctx context.Context, inbox *steer.Inbox) (context.Context, *SteerRun) {
	run := &SteerRun{inbox: inbox}
	return context.WithValue(ctx, steerRunKey{}, run), run
}

// Delivered returns IDs recorded by the model callback in delivery order.
// The caller decides whether to acknowledge them after processing the run.
func (r *SteerRun) Delivered() []entry.Seq {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return slices.Clone(r.delivered)
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
		return slices.Contains(r.delivered, m.ID)
	}), nil
}

type steerDeliveryOptions struct {
	run           *SteerRun
	mainSessionID string
	contents      []*genai.Content
}

func (a *TapeAdapter) deliverSteer(ctx context.Context, opts steerDeliveryOptions) ([]*genai.Content, error) {
	run := opts.run
	sidecar := run.inbox.Tape()
	if sidecar.OwnerID != a.Tape.OwnerID {
		return nil, fmt.Errorf("agent: steer sidecar owner %q does not match main tape owner %q", sidecar.OwnerID, a.Tape.OwnerID)
	}
	pending, err := run.pending(ctx)
	if err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		return opts.contents, nil
	}
	tapeCtx := a.tapeContext(ctx)
	source, err := sidecar.Get(tapeCtx)
	if err != nil {
		return nil, fmt.Errorf("agent: read steer sidecar session: %w", err)
	}
	if source.SessionID == opts.mainSessionID {
		return nil, fmt.Errorf("agent: steer sidecar must use a separate session from %q", opts.mainSessionID)
	}
	contents := opts.contents
	for _, message := range pending {
		delivery := entry.CustomEntry{
			Entry: entry.NewEntry(
				entry.WithEntryKind(entry.EntryUser),
				entry.WithEntryOwner(a.Tape.OwnerID),
				entry.WithEntryContent(message.Text),
			),
			Extensions: map[string]any{
				steerIDExtension:      message.ID.String(),
				steerSessionExtension: source.SessionID,
			},
		}
		if _, err := a.Tape.Store(tapeCtx, delivery); err != nil {
			return nil, fmt.Errorf("agent: persist steer %s: %w", message.ID, err)
		}
		contents = append(contents, genai.NewContentFromText(message.Text, genai.RoleUser))
		run.delivered = append(run.delivered, message.ID)
	}
	return contents, nil
}
