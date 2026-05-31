package skills

import (
	"context"
	"testing"

	"github.com/scbizu/tape-go/internal/tape"
)

type recordingHandoffWriter struct {
	called int
}

func (r *recordingHandoffWriter) Handoff(_ context.Context, sessionID string, input tape.HandoffInput) (*tape.Anchor, error) {
	r.called++
	return &tape.Anchor{
		ID:        sessionID + "-anchor-1",
		SessionID: sessionID,
		PhaseTag:  input.PhaseTag,
		Summary:   input.Summary,
	}, nil
}

type fakeHandoffWriter struct{}

func (fakeHandoffWriter) Handoff(_ context.Context, sessionID string, input tape.HandoffInput) (*tape.Anchor, error) {
	return &tape.Anchor{
		ID:        sessionID + "-anchor-1",
		SessionID: sessionID,
		PhaseTag:  input.PhaseTag,
		Summary:   input.Summary,
	}, nil
}

func TestHandoffBridgeCallsTapeHandoff(t *testing.T) {
	writer := &recordingHandoffWriter{}
	bridge := NewHandoffBridge(writer)

	_, err := bridge.Apply(context.Background(), "session-1", tape.HandoffInput{
		Summary:    "Discovery complete.",
		NextSteps:  []string{"Run migration"},
		SourceSeqs: []uint64{1},
		Owner:      "agent",
	})
	if err != nil {
		t.Fatalf("apply handoff bridge: %v", err)
	}

	if writer.called != 1 {
		t.Fatal("expected tape handoff to be called")
	}
}

func TestHandoffBridgeReturnsStructuredOutcome(t *testing.T) {
	bridge := NewHandoffBridge(fakeHandoffWriter{})

	result, err := bridge.Apply(context.Background(), "session-1", tape.HandoffInput{
		Summary: "Discovery complete.",
	})
	if err != nil {
		t.Fatalf("apply handoff bridge: %v", err)
	}

	if !result.HandoffWritten {
		t.Fatal("expected structured handoff outcome")
	}
	if !result.AnchorWritten {
		t.Fatal("expected anchor result to be marked written")
	}
}
