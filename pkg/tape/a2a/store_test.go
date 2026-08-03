package a2atape

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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

type observedStorage struct {
	storage.TapeStorage

	mu         sync.Mutex
	ranges     []view.EntryRange
	forcedHead *uint64
	blockOwner string
	entered    chan struct{}
	release    chan struct{}
	enterOnce  sync.Once
}

func (s *observedStorage) Get(ctx context.Context) (view.TapeView, error) {
	ownerID, _ := owner.GetOwnerId(ctx)
	s.mu.Lock()
	block := ownerID != "" && ownerID == s.blockOwner && s.entered != nil && s.release != nil
	forcedHead := s.forcedHead
	s.mu.Unlock()
	if block {
		s.enterOnce.Do(func() { close(s.entered) })
		<-s.release
	}
	tape, err := s.TapeStorage.Get(ctx)
	if err == nil && forcedHead != nil {
		tape.Scope.SeqE = *forcedHead
	}
	return tape, err
}

func (s *observedStorage) Range(ctx context.Context, r view.EntryRange, opts ...storage.RangeBy) (view.EntryView, error) {
	s.mu.Lock()
	s.ranges = append(s.ranges, r)
	s.mu.Unlock()
	return s.TapeStorage.Range(ctx, r, opts...)
}

func (s *observedStorage) resetRanges() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ranges = nil
}

func (s *observedStorage) observedRanges() []view.EntryRange {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]view.EntryRange(nil), s.ranges...)
}

func (s *observedStorage) Close() error {
	if closer, ok := s.TapeStorage.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
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

func TestNewStoreRejectsTypedNilStorage(t *testing.T) {
	var backend *jsonl.JSONL
	if _, err := NewStore(Config{Storage: backend, Authenticator: testAuthenticator}); err == nil {
		t.Fatal("NewStore() error = nil, want typed-nil storage validation")
	}
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

func TestStoreUpdateRejectsTypedNilEvent(t *testing.T) {
	backend := backendFactories(t)[0].open(t)
	defer closeBackend(t, backend)
	store := newTestStore(t, backend)
	ctx := authenticatedAs("owner-a")
	task := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
	version, err := store.Create(ctx, task)
	if err != nil {
		t.Fatal(err)
	}
	var event *a2a.TaskStatusUpdateEvent
	if _, err := store.Update(ctx, &taskstore.UpdateRequest{Task: task, Event: event, PrevVersion: version}); err == nil {
		t.Fatal("Update() error = nil, want typed-nil event validation")
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

func TestStoreReplayReadsOnlyNewEntries(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := &observedStorage{TapeStorage: factory.open(t)}
			defer closeBackend(t, backend)
			ctx := authenticatedAs("owner-a")
			store := newTestStore(t, backend)
			task1 := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			if _, err := store.Create(ctx, task1); err != nil {
				t.Fatal(err)
			}

			backend.resetRanges()
			if _, err := store.Get(ctx, task1.ID); err != nil {
				t.Fatal(err)
			}
			if ranges := backend.observedRanges(); len(ranges) != 0 {
				t.Fatalf("unchanged head triggered Range calls: %+v", ranges)
			}

			otherStore := newTestStore(t, backend)
			task2 := testTask("task-2", "context-2", a2a.TaskStateSubmitted)
			if _, err := otherStore.Create(ctx, task2); err != nil {
				t.Fatal(err)
			}
			backend.resetRanges()
			if _, err := store.Get(ctx, task2.ID); err != nil {
				t.Fatal(err)
			}
			want := view.EntryRange{SeqS: 2, SeqE: 3}
			if ranges := backend.observedRanges(); len(ranges) != 1 || ranges[0] != want {
				t.Fatalf("incremental ranges = %+v, want [%+v]", ranges, want)
			}
		})
	}
}

func TestStoreAllowsDifferentOwnersToReadConcurrently(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := &observedStorage{TapeStorage: factory.open(t)}
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			taskA := testTask("task-a", "context-a", a2a.TaskStateSubmitted)
			taskB := testTask("task-b", "context-b", a2a.TaskStateSubmitted)
			if _, err := store.Create(authenticatedAs("owner-a"), taskA); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Create(authenticatedAs("owner-b"), taskB); err != nil {
				t.Fatal(err)
			}

			backend.mu.Lock()
			backend.blockOwner = "owner-a"
			backend.entered = make(chan struct{})
			backend.release = make(chan struct{})
			backend.mu.Unlock()
			errA := make(chan error, 1)
			errB := make(chan error, 1)
			go func() {
				_, err := store.Get(authenticatedAs("owner-a"), taskA.ID)
				errA <- err
			}()
			select {
			case <-backend.entered:
			case <-time.After(time.Second):
				t.Fatal("owner A did not reach blocked storage read")
			}
			go func() {
				_, err := store.Get(authenticatedAs("owner-b"), taskB.ID)
				errB <- err
			}()

			select {
			case err := <-errB:
				if err != nil {
					t.Fatalf("owner B Get() error = %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				close(backend.release)
				<-errA
				t.Fatal("owner B was serialized behind owner A")
			}
			close(backend.release)
			if err := <-errA; err != nil {
				t.Fatalf("owner A Get() error = %v", err)
			}
		})
	}
}

func TestStoreReplayInvalidatesCacheWhenHeadRegresses(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := &observedStorage{TapeStorage: factory.open(t)}
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			ctx := authenticatedAs("owner-a")
			task := testTask("task-1", "context-1", a2a.TaskStateSubmitted)
			if _, err := store.Create(ctx, task); err != nil {
				t.Fatal(err)
			}

			regressed := uint64(0)
			backend.mu.Lock()
			backend.forcedHead = &regressed
			backend.mu.Unlock()
			if _, err := store.Get(ctx, task.ID); err == nil || !strings.Contains(err.Error(), "head regressed") {
				t.Fatalf("Get() error = %v, want head regression", err)
			}

			backend.mu.Lock()
			backend.forcedHead = nil
			backend.mu.Unlock()
			if _, err := store.Get(ctx, task.ID); err != nil {
				t.Fatalf("Get() after cache invalidation error = %v", err)
			}
		})
	}
}

func TestStoreListDefaultsAndOwnerIsolation(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			for _, task := range []*a2a.Task{
				testTask("task-a1", "context-a", a2a.TaskStateSubmitted),
				testTask("task-a2", "context-a", a2a.TaskStateWorking),
			} {
				if _, err := store.Create(authenticatedAs("owner-a"), task); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := store.Create(authenticatedAs("owner-b"), testTask("task-b", "context-b", a2a.TaskStateSubmitted)); err != nil {
				t.Fatal(err)
			}

			got, err := store.List(authenticatedAs("owner-a"), &a2a.ListTasksRequest{})
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if got.PageSize != 50 || got.TotalSize != 2 || len(got.Tasks) != 2 {
				t.Fatalf("List() = %#v, want owner A's 2 tasks with default page size 50", got)
			}
			for _, task := range got.Tasks {
				if task.ID == "task-b" {
					t.Fatal("List() leaked owner B's task")
				}
			}
		})
	}
}

func TestStoreListFilters(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			store := newTestStore(t, backend)
			oldTime := time.Date(2026, 8, 3, 6, 0, 0, 0, time.UTC)
			newTime := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
			cutoff := time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)
			old := testTask("task-old", "context-x", a2a.TaskStateWorking)
			old.Status.Timestamp = &oldTime
			matched := testTask("task-match", "context-x", a2a.TaskStateWorking)
			matched.Status.Timestamp = &newTime
			wrongContext := testTask("task-context", "context-y", a2a.TaskStateWorking)
			wrongContext.Status.Timestamp = &newTime
			wrongStatus := testTask("task-status", "context-x", a2a.TaskStateCompleted)
			wrongStatus.Status.Timestamp = &newTime
			for _, task := range []*a2a.Task{old, matched, wrongContext, wrongStatus} {
				if _, err := store.Create(authenticatedAs("owner-a"), task); err != nil {
					t.Fatal(err)
				}
			}

			got, err := store.List(authenticatedAs("owner-a"), &a2a.ListTasksRequest{
				ContextID:            "context-x",
				Status:               a2a.TaskStateWorking,
				StatusTimestampAfter: &cutoff,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Tasks) != 1 || got.Tasks[0].ID != matched.ID || got.TotalSize != 1 {
				t.Fatalf("filtered List() = %#v, want only %q", got, matched.ID)
			}
		})
	}
}

func TestStoreListRejectsInvalidPageSize(t *testing.T) {
	backend := backendFactories(t)[0].open(t)
	defer closeBackend(t, backend)
	store := newTestStore(t, backend)
	for _, pageSize := range []int{-1, 101} {
		_, err := store.List(authenticatedAs("owner-a"), &a2a.ListTasksRequest{PageSize: pageSize})
		if !errors.Is(err, a2a.ErrInvalidRequest) {
			t.Fatalf("List(PageSize=%d) error = %v, want ErrInvalidRequest", pageSize, err)
		}
	}
}

func TestStoreListPaginatesByRecordTimeThenTaskID(t *testing.T) {
	for _, factory := range backendFactories(t) {
		t.Run(factory.name, func(t *testing.T) {
			backend := factory.open(t)
			defer closeBackend(t, backend)
			times := []time.Time{
				time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC),
				time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC),
				time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC),
			}
			store, err := NewStore(Config{
				Storage:       backend,
				Authenticator: testAuthenticator,
				TimeProvider: func() time.Time {
					next := times[0]
					times = times[1:]
					return next
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			ctx := authenticatedAs("owner-a")
			for _, id := range []a2a.TaskID{"task-a", "task-z", "task-b"} {
				if _, err := store.Create(ctx, testTask(id, "context-1", a2a.TaskStateSubmitted)); err != nil {
					t.Fatal(err)
				}
			}

			first, err := store.List(ctx, &a2a.ListTasksRequest{PageSize: 2})
			if err != nil {
				t.Fatal(err)
			}
			if got := taskIDs(first.Tasks); !reflect.DeepEqual(got, []a2a.TaskID{"task-b", "task-z"}) {
				t.Fatalf("first page IDs = %v, want [task-b task-z]", got)
			}
			if first.TotalSize != 3 || first.NextPageToken == "" {
				t.Fatalf("first page metadata = %#v", first)
			}
			second, err := store.List(ctx, &a2a.ListTasksRequest{PageSize: 2, PageToken: first.NextPageToken})
			if err != nil {
				t.Fatal(err)
			}
			if got := taskIDs(second.Tasks); !reflect.DeepEqual(got, []a2a.TaskID{"task-a"}) || second.NextPageToken != "" {
				t.Fatalf("second page = %#v", second)
			}
		})
	}
}

func taskIDs(tasks []*a2a.Task) []a2a.TaskID {
	ids := make([]a2a.TaskID, len(tasks))
	for i, task := range tasks {
		ids[i] = task.ID
	}
	return ids
}

func TestListItemOrderingUsesTaskIDForTimestampTies(t *testing.T) {
	updatedAt := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	z := listItem{stored: &taskstore.StoredTask{Task: testTask("task-z", "context-1", a2a.TaskStateSubmitted)}, updatedAt: updatedAt}
	b := listItem{stored: &taskstore.StoredTask{Task: testTask("task-b", "context-1", a2a.TaskStateSubmitted)}, updatedAt: updatedAt}
	if got := compareListItems(z, b); got >= 0 {
		t.Fatalf("compareListItems(task-z, task-b) = %d, want task-z first", got)
	}
}

func TestStoreListTrimsHistoryAndArtifactsOnCopies(t *testing.T) {
	backend := backendFactories(t)[0].open(t)
	defer closeBackend(t, backend)
	store := newTestStore(t, backend)
	ctx := authenticatedAs("owner-a")
	task := testTask("task-1", "context-1", a2a.TaskStateCompleted)
	task.History = []*a2a.Message{{ID: "m1"}, {ID: "m2"}, {ID: "m3"}}
	task.Artifacts = []*a2a.Artifact{{ID: "artifact-1"}}
	if _, err := store.Create(ctx, task); err != nil {
		t.Fatal(err)
	}
	historyLength := 2

	got, err := store.List(ctx, &a2a.ListTasksRequest{HistoryLength: &historyLength})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Tasks) != 1 || taskIDsFromHistory(got.Tasks[0].History) != "m2,m3" {
		t.Fatalf("trimmed history = %#v", got.Tasks)
	}
	if got.Tasks[0].Artifacts != nil {
		t.Fatalf("artifacts = %#v, want nil", got.Tasks[0].Artifacts)
	}
	stored, err := store.Get(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Task.History) != 3 || len(stored.Task.Artifacts) != 1 {
		t.Fatalf("List() mutated stored task: %#v", stored.Task)
	}

	withArtifacts, err := store.List(ctx, &a2a.ListTasksRequest{IncludeArtifacts: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(withArtifacts.Tasks[0].Artifacts) != 1 {
		t.Fatalf("IncludeArtifacts result = %#v", withArtifacts.Tasks[0].Artifacts)
	}
}

func taskIDsFromHistory(history []*a2a.Message) string {
	ids := make([]string, len(history))
	for i, message := range history {
		ids[i] = message.ID
	}
	return strings.Join(ids, ",")
}
