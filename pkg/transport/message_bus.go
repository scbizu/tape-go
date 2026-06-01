package transport

import (
	"context"
	"errors"
)

type BusEventType string

const (
	BusEventMessage BusEventType = "message"
	BusEventCommand BusEventType = "command"
	BusEventCallback BusEventType = "callback"
	BusEventSystem BusEventType = "system"
)

type BusState string

const (
	StateReceived  BusState = "received"
	StateValidated BusState = "validated"
	StateRouted    BusState = "routed"
	StateExecuting BusState = "executing"
	StateReplied   BusState = "replied"
	StateFailed    BusState = "failed"
)

type BusEvent struct {
	Type      BusEventType
	SessionID string
	UserID    string
	Content   string
}

type MessageHandler interface {
	HandleMessage(context.Context, BusEvent) error
}

type BusRouter struct {
	handler MessageHandler
}

func NewBusRouter(handler MessageHandler) *BusRouter {
	return &BusRouter{handler: handler}
}

func (r *BusRouter) Handle(ctx context.Context, evt BusEvent) (BusState, error) {
	if evt.SessionID == "" || evt.UserID == "" {
		return StateFailed, errors.New("session_id and user_id are required")
	}
	if evt.Type != BusEventMessage {
		return StateFailed, errors.New("unsupported event type")
	}
	if err := r.handler.HandleMessage(ctx, evt); err != nil {
		return StateFailed, err
	}
	return StateReplied, nil
}
