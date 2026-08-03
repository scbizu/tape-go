package a2atape

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
)

type principalKey struct{}

func authenticatedAs(principal string) context.Context {
	return context.WithValue(context.Background(), principalKey{}, principal)
}

func testAuthenticator(ctx context.Context) (string, error) {
	principal, _ := ctx.Value(principalKey{}).(string)
	return principal, nil
}

type backendFactory struct {
	name string
	open func(t *testing.T) storage.TapeStorage
}

func backendFactories(t *testing.T) []backendFactory {
	t.Helper()
	jsonlDir := filepath.Join(t.TempDir(), "jsonl")
	bboltPath := filepath.Join(t.TempDir(), "tape.db")
	return []backendFactory{
		{
			name: "jsonl",
			open: func(t *testing.T) storage.TapeStorage {
				t.Helper()
				backend, err := jsonl.NewJSONLStorage("a2a", jsonlDir)
				if err != nil {
					t.Fatal(err)
				}
				return backend
			},
		},
		{
			name: "bbolt",
			open: func(t *testing.T) storage.TapeStorage {
				t.Helper()
				backend, err := bbolt.NewBboltStorage("a2a", bboltPath)
				if err != nil {
					t.Fatal(err)
				}
				return backend
			},
		},
	}
}

func closeBackend(t *testing.T, backend storage.TapeStorage) {
	t.Helper()
	if closer, ok := backend.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
	}
}

func newTestStore(t *testing.T, backend storage.TapeStorage) *Store {
	t.Helper()
	store, err := NewStore(Config{
		Storage:       backend,
		Authenticator: testAuthenticator,
		TimeProvider: func() time.Time {
			return time.Date(2026, 8, 3, 7, 30, 0, 0, time.UTC)
		},
	})
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	return store
}

func TestStoreCreateAndGet(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			ctx := authenticatedAs("owner-a")
			task := testTask("task-1", "context-1", a2a.TaskStateSubmitted)

			version, err := store.Create(ctx, task)
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if version == taskstore.TaskVersionMissing {
				t.Fatal("Create() version is missing")
			}
			got, err := store.Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if got.Version != version || !reflect.DeepEqual(got.Task, task) {
				t.Fatalf("Get() = %#v, want task %#v at version %d", got, task, version)
			}

			got.Task.Status.State = a2a.TaskStateFailed
			again, err := store.Get(ctx, task.ID)
			if err != nil {
				t.Fatal(err)
			}
			if again.Task.Status.State != a2a.TaskStateSubmitted {
				t.Fatal("Get() returned mutable store state")
			}

			if _, err := store.Create(ctx, task); !errors.Is(err, taskstore.ErrTaskAlreadyExists) {
				t.Fatalf("duplicate Create() error = %v, want ErrTaskAlreadyExists", err)
			}
			if _, err := store.Get(ctx, "missing"); !errors.Is(err, a2a.ErrTaskNotFound) {
				t.Fatalf("missing Get() error = %v, want ErrTaskNotFound", err)
			}
		})
	}
}

func TestStoreRecovery(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			ctx := authenticatedAs("owner-a")
			task := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			firstBackend := factory.open(t)
			version, err := newTestStore(t, firstBackend).Create(ctx, task)
			if err != nil {
				t.Fatal(err)
			}
			closeBackend(t, firstBackend)

			secondBackend := factory.open(t)
			defer closeBackend(t, secondBackend)
			got, err := newTestStore(t, secondBackend).Get(ctx, task.ID)
			if err != nil {
				t.Fatalf("Get() after reopen error = %v", err)
			}
			if got.Version != version || !reflect.DeepEqual(got.Task, task) {
				t.Fatalf("Get() after reopen = %#v, want task %#v at version %d", got, task, version)
			}
		})
	}
}

func TestStoreOwnerIsolation(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			taskA := testTask("shared-id", "context-a", a2a.TaskStateSubmitted)
			if _, err := store.Create(authenticatedAs("owner-a"), taskA); err != nil {
				t.Fatal(err)
			}

			if _, err := store.Get(authenticatedAs("owner-b"), taskA.ID); !errors.Is(err, a2a.ErrTaskNotFound) {
				t.Fatalf("owner B Get() error = %v, want ErrTaskNotFound", err)
			}
			taskB := testTask("shared-id", "context-b", a2a.TaskStateWorking)
			if _, err := store.Create(authenticatedAs("owner-b"), taskB); err != nil {
				t.Fatalf("owner B Create() with same task ID error = %v", err)
			}
			gotA, err := store.Get(authenticatedAs("owner-a"), taskA.ID)
			if err != nil || gotA.Task.ContextID != "context-a" {
				t.Fatalf("owner A task changed: got %#v, err %v", gotA, err)
			}
		})
	}
}
