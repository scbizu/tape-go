package transport

import (
	"context"
	"testing"
)

type fakeHandler struct{}

func (fakeHandler) HandleMessage(context.Context, BusEvent) error {
	return nil
}

func TestBusRouter_MessageFlowState(t *testing.T) {
	r := NewBusRouter(fakeHandler{})
	evt := BusEvent{Type: BusEventMessage, SessionID: "s1", UserID: "u1", Content: "hi"}
	state, err := r.Handle(context.Background(), evt)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if state != StateReplied {
		t.Fatalf("expected %s, got %s", StateReplied, state)
	}
}
