package a2atape

import (
	"context"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/scbizu/tape-go/pkg/tape/owner"
)

func TestPersistenceInterceptorPersistsTasklessMessageOnce(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			interceptor := NewPersistenceInterceptor(newTestStore(t, backend))
			ctx := authenticatedAs("owner-a")
			message := &a2a.Message{ID: "message-1", Role: a2a.MessageRoleAgent}
			response := &a2asrv.Response{Payload: message}

			if err := interceptor.After(ctx, nil, response); err != nil {
				t.Fatalf("After() error = %v", err)
			}
			if err := interceptor.After(ctx, nil, response); err != nil {
				t.Fatalf("After() retry error = %v", err)
			}

			tape, err := backend.Get(owner.WithOwnerId(context.Background(), "owner-a"))
			if err != nil {
				t.Fatal(err)
			}
			if tape.Scope.SeqE != 1 {
				t.Fatalf("last seq = %d, want 1; direct message was not deduplicated", tape.Scope.SeqE)
			}
		})
	}
}

func TestPersistenceInterceptorSkipsNonDirectResponses(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			interceptor := NewPersistenceInterceptor(newTestStore(t, backend))
			ctx := authenticatedAs("owner-a")
			ownerCtx := owner.WithOwnerId(context.Background(), "owner-a")
			if err := backend.Init(ownerCtx); err != nil {
				t.Fatal(err)
			}
			task := testTask("task-1", "context-1", a2a.TaskStateSubmitted)

			for _, response := range []*a2asrv.Response{
				{Payload: task},
				{Err: context.Canceled},
				{Payload: &a2a.Message{ID: "task-message", TaskID: task.ID, ContextID: task.ContextID, Role: a2a.MessageRoleAgent}},
			} {
				if err := interceptor.After(ctx, nil, response); err != nil {
					t.Fatalf("After() error = %v", err)
				}
			}

			tape, err := backend.Get(ownerCtx)
			if err != nil {
				t.Fatal(err)
			}
			if tape.Scope.SeqE != 0 {
				t.Fatalf("last seq = %d, want 0; non-direct response was persisted", tape.Scope.SeqE)
			}
		})
	}
}
