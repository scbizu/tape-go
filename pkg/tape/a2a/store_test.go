package a2atape

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
	"github.com/scbizu/tape-go/pkg/tape/view"
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

func TestStoreUpdateUsesOCC(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			ctx := authenticatedAs("owner-a")
			previous := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			version, err := store.Create(ctx, previous)
			if err != nil {
				t.Fatal(err)
			}
			desired := testTask("task-1", "context-1", a2a.TaskStateWorking)
			event := a2a.NewStatusUpdateEvent(desired, a2a.TaskStateWorking, nil)

			_, err = store.Update(ctx, &taskstore.UpdateRequest{
				Task:        desired,
				Event:       event,
				PrevTask:    previous,
				PrevVersion: version + 1,
			})
			if !errors.Is(err, taskstore.ErrConcurrentModification) {
				t.Fatalf("Update() error = %v, want ErrConcurrentModification", err)
			}
		})
	}
}

func TestStoreUpdateRetryHasNoSecondEffect(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			ctx := authenticatedAs("owner-a")
			previous := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			version, err := store.Create(ctx, previous)
			if err != nil {
				t.Fatal(err)
			}
			desired := testTask("task-1", "context-1", a2a.TaskStateWorking)
			event := a2a.NewStatusUpdateEvent(desired, a2a.TaskStateWorking, nil)
			event.Status.Timestamp = desired.Status.Timestamp
			request := &taskstore.UpdateRequest{Task: desired, Event: event, PrevTask: previous, PrevVersion: version}

			first, err := store.Update(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			second, err := store.Update(ctx, request)
			if err != nil {
				t.Fatalf("retry Update() error = %v", err)
			}
			if second != first {
				t.Fatalf("retry version = %d, want %d", second, first)
			}
			tape, err := backend.Get(owner.WithOwnerId(context.Background(), "owner-a"))
			if err != nil {
				t.Fatal(err)
			}
			if tape.Scope.SeqE != uint64(first) {
				t.Fatalf("last seq = %d, want %d; retry appended another record", tape.Scope.SeqE, first)
			}
		})
	}
}

func TestStoreUpdatePersistsTaskAndEventAtomically(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			ctx := authenticatedAs("owner-a")
			previous := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			desired := testTask("task-1", "context-1", a2a.TaskStateCompleted)
			event := a2a.NewStatusUpdateEvent(desired, a2a.TaskStateCompleted, nil)
			event.Status.Timestamp = desired.Status.Timestamp

			firstBackend := factory.open(t)
			firstStore := newTestStore(t, firstBackend)
			version, err := firstStore.Create(ctx, previous)
			if err != nil {
				t.Fatal(err)
			}
			updatedVersion, err := firstStore.Update(ctx, &taskstore.UpdateRequest{
				Task: desired, Event: event, PrevTask: previous, PrevVersion: version,
			})
			if err != nil {
				t.Fatal(err)
			}
			closeBackend(t, firstBackend)

			secondBackend := factory.open(t)
			defer closeBackend(t, secondBackend)
			got, err := newTestStore(t, secondBackend).Get(ctx, desired.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Version != updatedVersion || !reflect.DeepEqual(got.Task, desired) {
				t.Fatalf("recovered task = %#v, want %#v at %d", got, desired, updatedVersion)
			}
			ownerCtx := owner.WithOwnerId(context.Background(), "owner-a")
			entries, err := secondBackend.Range(ownerCtx, view.EntryRange{SeqS: uint64(updatedVersion), SeqE: uint64(updatedVersion) + 1})
			if err != nil || len(entries.Raw) != 1 {
				t.Fatalf("read update entry: len=%d err=%v", len(entries.Raw), err)
			}
			record, err := recordFromEntry(entries.Raw[0])
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(record.Task, desired) || !reflect.DeepEqual(record.Event.Event, event) {
				t.Fatalf("atomic record mismatch: %#v", record)
			}
		})
	}
}

func TestStoreReplayFailsClosedOnCorruptRecord(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			ownerCtx := owner.WithOwnerId(context.Background(), "owner-a")
			if err := backend.Init(ownerCtx); err != nil {
				t.Fatal(err)
			}
			corrupt := entry.CustomEntry{
				Entry: entry.NewEntry(
					entry.WithEntryKind(kindTask),
					entry.WithEntryOwner("owner-a"),
				),
				Extensions: map[string]any{
					recordExtension: `{"profileVersion":99,"recordId":"corrupt-1","owner":"owner-a","kind":"a2a:task"}`,
				},
			}
			if err := backend.Store(ownerCtx, corrupt); err != nil {
				t.Fatal(err)
			}

			_, err := newTestStore(t, backend).Get(authenticatedAs("owner-a"), "task-1")
			if err == nil || !strings.Contains(err.Error(), "seq 1") || !strings.Contains(err.Error(), "corrupt-1") {
				t.Fatalf("Get() error = %v, want seq and record identity", err)
			}
		})
	}
}
