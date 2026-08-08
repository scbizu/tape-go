package a2atape

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/cucumber/godog"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/storage/bbolt"
	"github.com/scbizu/tape-go/pkg/tape/storage/jsonl"
)

func TestFeatures(t *testing.T) {
	suite := godog.TestSuite{
		ScenarioInitializer: initializeTapeStoreScenario,
		Options: &godog.Options{
			Format:   "pretty",
			Paths:    []string{"features"},
			Strict:   true,
			TestingT: t,
		},
	}
	if suite.Run() != 0 {
		t.Fatal("non-zero status returned, failed to run feature tests")
	}
}

type featureState struct {
	cases []*featureCase

	parent         *featureCase
	child          *featureCase
	server         *httptest.Server
	client         *a2aclient.Client
	delegatedTask  *a2a.Task
	recoveredChild *a2a.Task
}

type featureCase struct {
	kind    string
	root    string
	backend storage.TapeStorage
	store   *Store
	ctx     context.Context
	auth    taskstore.Authenticator

	task            *a2a.Task
	expectedTask    *a2a.Task
	expectedVersion taskstore.TaskVersion

	competingSuccesses int
	competingConflicts int
	retryVersion       taskstore.TaskVersion
	headBeforeRetry    uint64
	headAfterRetry     uint64

	principalA context.Context
	principalB context.Context
}

func initializeTapeStoreScenario(sc *godog.ScenarioContext) {
	state := &featureState{}
	sc.Before(func(ctx context.Context, _ *godog.Scenario) (context.Context, error) {
		*state = featureState{}
		return ctx, nil
	})
	sc.After(func(ctx context.Context, _ *godog.Scenario, _ error) (context.Context, error) {
		return ctx, state.close()
	})

	sc.Step(`^a (jsonl|bbolt) TapeTaskStore$`, state.aTapeTaskStore)
	sc.Step(`^an A2A task advances to (working|input_required|completed) and the store restarts$`, state.taskAdvancesAndStoreRestarts)
	sc.Step(`^GetTask returns the same task, history, artifacts and version$`, state.getTaskReturnsSameProjection)
	sc.Step(`^a stored task version$`, state.aStoredTaskVersion)
	sc.Step(`^two updates use that version and one accepted record is retried$`, state.concurrentUpdatesAndRetry)
	sc.Step(`^exactly one competing update succeeds and the retry has no second effect$`, state.assertConcurrentUpdateOutcome)
	sc.Step(`^two authenticated principals use the same task id$`, state.twoPrincipalsUseSameTaskID)
	sc.Step(`^neither principal can Get or List the other principal's task$`, state.assertTenantIsolation)
	sc.Step(`^parent and child agents use independent Tapes$`, state.parentAndChildUseIndependentTapes)
	sc.Step(`^the parent delegates through JSON-RPC$`, state.parentDelegatesThroughJSONRPC)
	sc.Step(`^the child task remains queryable after restart$`, state.childTaskQueryableAfterRestart)
	sc.Step(`^the parent can consume the child artifact$`, state.parentConsumesChildArtifact)
}

func (s *featureState) close() error {
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	var joined error
	for _, testCase := range s.cases {
		if err := closeFeatureBackend(testCase.backend); err != nil {
			joined = errors.Join(joined, err)
		}
		if testCase.root != "" {
			if err := os.RemoveAll(testCase.root); err != nil {
				joined = errors.Join(joined, err)
			}
		}
	}
	return joined
}

type deterministicFeatureExecutor struct{}

func (*deterministicFeatureExecutor) Execute(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		if !yield(a2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		artifact := a2a.NewArtifactEvent(execCtx, a2a.NewTextPart("child-artifact"))
		artifact.Artifact.ID = "artifact-1"
		artifact.Artifact.Name = "delegated-result"
		if !yield(artifact, nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCompleted, nil), nil)
	}
}

func (*deterministicFeatureExecutor) Cancel(_ context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(a2a.NewStatusUpdateEvent(execCtx, a2a.TaskStateCanceled, nil), nil)
	}
}

func (s *featureState) aTapeTaskStore(kind string) error {
	testCase, err := newFeatureCase(kind, "owner-a")
	if err != nil {
		return err
	}
	s.cases = []*featureCase{testCase}
	return nil
}

func (s *featureState) taskAdvancesAndStoreRestarts(stateName string) error {
	if len(s.cases) != 1 {
		return errors.New("feature: TapeTaskStore is not initialized")
	}
	testCase := s.cases[0]
	state, err := featureTaskState(stateName)
	if err != nil {
		return err
	}
	previous := testTask("task-restart", "context-restart", a2a.TaskStateSubmitted)
	version, err := testCase.store.Create(testCase.ctx, previous)
	if err != nil {
		return err
	}
	desired := testTask(previous.ID, previous.ContextID, state)
	desired.History = []*a2a.Message{
		{ID: "message-1", TaskID: desired.ID, ContextID: desired.ContextID, Role: a2a.MessageRoleUser},
		{ID: "message-2", TaskID: desired.ID, ContextID: desired.ContextID, Role: a2a.MessageRoleAgent},
	}
	desired.Artifacts = []*a2a.Artifact{{ID: "artifact-1", Name: "result"}}
	event := a2a.NewStatusUpdateEvent(desired, state, nil)
	event.Status.Timestamp = desired.Status.Timestamp
	updatedVersion, err := testCase.store.Update(testCase.ctx, &taskstore.UpdateRequest{
		Task: desired, Event: event, PrevTask: previous, PrevVersion: version,
	})
	if err != nil {
		return err
	}
	testCase.task = desired
	testCase.expectedTask = desired
	testCase.expectedVersion = updatedVersion
	return testCase.reopen()
}

func (s *featureState) getTaskReturnsSameProjection() error {
	if len(s.cases) != 1 {
		return errors.New("feature: restart case is not initialized")
	}
	testCase := s.cases[0]
	got, err := testCase.store.Get(testCase.ctx, testCase.task.ID)
	if err != nil {
		return err
	}
	if got.Version != testCase.expectedVersion || !reflect.DeepEqual(got.Task, testCase.expectedTask) {
		return fmt.Errorf("feature: recovered task/version mismatch: got %#v, want task %#v at version %d", got, testCase.expectedTask, testCase.expectedVersion)
	}
	return nil
}

func (s *featureState) aStoredTaskVersion() error {
	for _, kind := range []string{"jsonl", "bbolt"} {
		testCase, err := newFeatureCase(kind, "owner-a")
		if err != nil {
			return err
		}
		task := testTask("task-concurrent", "context-concurrent", a2a.TaskStateSubmitted)
		version, err := testCase.store.Create(testCase.ctx, task)
		if err != nil {
			return err
		}
		testCase.task = task
		testCase.expectedVersion = version
		s.cases = append(s.cases, testCase)
	}
	return nil
}

func (s *featureState) concurrentUpdatesAndRetry() error {
	for _, testCase := range s.cases {
		working := testTask(testCase.task.ID, testCase.task.ContextID, a2a.TaskStateWorking)
		workingEvent := a2a.NewStatusUpdateEvent(working, a2a.TaskStateWorking, nil)
		workingEvent.Status.Timestamp = working.Status.Timestamp
		completed := testTask(testCase.task.ID, testCase.task.ContextID, a2a.TaskStateCompleted)
		completedEvent := a2a.NewStatusUpdateEvent(completed, a2a.TaskStateCompleted, nil)
		completedEvent.Status.Timestamp = completed.Status.Timestamp
		requests := []*taskstore.UpdateRequest{
			{Task: working, Event: workingEvent, PrevTask: testCase.task, PrevVersion: testCase.expectedVersion},
			{Task: completed, Event: completedEvent, PrevTask: testCase.task, PrevVersion: testCase.expectedVersion},
		}

		type result struct {
			request *taskstore.UpdateRequest
			version taskstore.TaskVersion
			err     error
		}
		start := make(chan struct{})
		results := make(chan result, len(requests))
		var ready sync.WaitGroup
		ready.Add(len(requests))
		for _, request := range requests {
			request := request
			go func() {
				ready.Done()
				<-start
				version, err := testCase.store.Update(testCase.ctx, request)
				results <- result{request: request, version: version, err: err}
			}()
		}
		ready.Wait()
		close(start)

		var accepted result
		for range requests {
			outcome := <-results
			switch {
			case outcome.err == nil:
				testCase.competingSuccesses++
				accepted = outcome
			case errors.Is(outcome.err, taskstore.ErrConcurrentModification):
				testCase.competingConflicts++
			default:
				return fmt.Errorf("feature: unexpected competing update error: %w", outcome.err)
			}
		}
		ownerCtx := owner.WithOwnerId(context.Background(), "owner-a")
		tape, err := testCase.backend.Get(ownerCtx)
		if err != nil {
			return err
		}
		testCase.headBeforeRetry = tape.Scope.SeqE
		testCase.retryVersion, err = testCase.store.Update(testCase.ctx, accepted.request)
		if err != nil {
			return fmt.Errorf("feature: accepted update retry: %w", err)
		}
		if testCase.retryVersion != accepted.version {
			return fmt.Errorf("feature: retry version %d, want %d", testCase.retryVersion, accepted.version)
		}
		tape, err = testCase.backend.Get(ownerCtx)
		if err != nil {
			return err
		}
		testCase.headAfterRetry = tape.Scope.SeqE
	}
	return nil
}

func (s *featureState) assertConcurrentUpdateOutcome() error {
	for _, testCase := range s.cases {
		if testCase.competingSuccesses != 1 || testCase.competingConflicts != 1 {
			return fmt.Errorf("feature: %s outcomes successes=%d conflicts=%d", testCase.kind, testCase.competingSuccesses, testCase.competingConflicts)
		}
		if testCase.headAfterRetry != testCase.headBeforeRetry {
			return fmt.Errorf("feature: %s retry appended a record: head %d -> %d", testCase.kind, testCase.headBeforeRetry, testCase.headAfterRetry)
		}
	}
	return nil
}

func (s *featureState) twoPrincipalsUseSameTaskID() error {
	for _, kind := range []string{"jsonl", "bbolt"} {
		testCase, err := newFeatureCase(kind, "owner-a")
		if err != nil {
			return err
		}
		testCase.principalA = authenticatedAs("owner-a")
		testCase.principalB = authenticatedAs("owner-b")
		taskA := testTask("shared-task", "context-a", a2a.TaskStateSubmitted)
		taskB := testTask("shared-task", "context-b", a2a.TaskStateWorking)
		if _, err := testCase.store.Create(testCase.principalA, taskA); err != nil {
			return err
		}
		if _, err := testCase.store.Create(testCase.principalB, taskB); err != nil {
			return err
		}
		s.cases = append(s.cases, testCase)
	}
	return nil
}

func (s *featureState) assertTenantIsolation() error {
	for _, testCase := range s.cases {
		gotA, err := testCase.store.Get(testCase.principalA, "shared-task")
		if err != nil {
			return err
		}
		gotB, err := testCase.store.Get(testCase.principalB, "shared-task")
		if err != nil {
			return err
		}
		if gotA.Task.ContextID != "context-a" || gotB.Task.ContextID != "context-b" {
			return fmt.Errorf("feature: %s cross-tenant Get leak: A=%q B=%q", testCase.kind, gotA.Task.ContextID, gotB.Task.ContextID)
		}
		listA, err := testCase.store.List(testCase.principalA, &a2a.ListTasksRequest{})
		if err != nil {
			return err
		}
		listB, err := testCase.store.List(testCase.principalB, &a2a.ListTasksRequest{})
		if err != nil {
			return err
		}
		if len(listA.Tasks) != 1 || len(listB.Tasks) != 1 || listA.Tasks[0].ContextID != "context-a" || listB.Tasks[0].ContextID != "context-b" {
			return fmt.Errorf("feature: %s cross-tenant List leak: A=%#v B=%#v", testCase.kind, listA.Tasks, listB.Tasks)
		}
	}
	return nil
}

func (s *featureState) parentAndChildUseIndependentTapes() error {
	parent, err := newFeatureCase("jsonl", "parent-owner")
	if err != nil {
		return err
	}
	childAuth := taskstore.Authenticator(func(context.Context) (string, error) { return "child-owner", nil })
	child, err := newFeatureCaseWithAuthenticator("bbolt", context.Background(), childAuth)
	if err != nil {
		_ = closeFeatureBackend(parent.backend)
		_ = os.RemoveAll(parent.root)
		return err
	}
	if parent.root == child.root || parent.backend == child.backend {
		return errors.New("feature: parent and child Tapes are not independent")
	}
	s.parent = parent
	s.child = child
	s.cases = []*featureCase{parent, child}
	return s.startChildServer()
}

func (s *featureState) parentDelegatesThroughJSONRPC() error {
	if s.client == nil {
		return errors.New("feature: JSON-RPC client is not initialized")
	}
	message := a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart("delegate this work"))
	result, err := s.client.SendMessage(context.Background(), &a2a.SendMessageRequest{Message: message})
	if err != nil {
		return fmt.Errorf("feature: JSON-RPC SendMessage: %w", err)
	}
	task, ok := result.(*a2a.Task)
	if !ok {
		return fmt.Errorf("feature: JSON-RPC result type %T, want *a2a.Task", result)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		return fmt.Errorf("feature: delegated task state %q, want completed", task.Status.State)
	}
	s.delegatedTask = task
	return nil
}

func (s *featureState) childTaskQueryableAfterRestart() error {
	if s.child == nil || s.delegatedTask == nil {
		return errors.New("feature: delegated child task is not initialized")
	}
	if s.server != nil {
		s.server.Close()
		s.server = nil
	}
	if err := s.child.reopen(); err != nil {
		return fmt.Errorf("feature: reopen child Tape: %w", err)
	}
	if err := s.startChildServer(); err != nil {
		return err
	}
	recovered, err := s.client.GetTask(context.Background(), &a2a.GetTaskRequest{ID: s.delegatedTask.ID})
	if err != nil {
		return fmt.Errorf("feature: GetTask after restart: %w", err)
	}
	if !reflect.DeepEqual(recovered, s.delegatedTask) {
		return fmt.Errorf("feature: child task changed after restart: got %#v, want %#v", recovered, s.delegatedTask)
	}
	s.recoveredChild = recovered
	return nil
}

func (s *featureState) parentConsumesChildArtifact() error {
	if s.parent == nil || s.recoveredChild == nil {
		return errors.New("feature: parent or recovered child task is not initialized")
	}
	if len(s.recoveredChild.Artifacts) != 1 || len(s.recoveredChild.Artifacts[0].Parts) != 1 {
		return fmt.Errorf("feature: child artifacts = %#v, want one artifact part", s.recoveredChild.Artifacts)
	}
	if got := s.recoveredChild.Artifacts[0].Parts[0].Text(); got != "child-artifact" {
		return fmt.Errorf("feature: child artifact text %q, want %q", got, "child-artifact")
	}
	consumed := testTask("parent-consumed", "parent-context", a2a.TaskStateCompleted)
	consumed.Artifacts = s.recoveredChild.Artifacts
	if _, err := s.parent.store.Create(s.parent.ctx, consumed); err != nil {
		return fmt.Errorf("feature: persist parent consumption: %w", err)
	}
	stored, err := s.parent.store.Get(s.parent.ctx, consumed.ID)
	if err != nil {
		return err
	}
	if len(stored.Task.Artifacts) != 1 || stored.Task.Artifacts[0].Parts[0].Text() != "child-artifact" {
		return errors.New("feature: parent could not consume child artifact")
	}
	return nil
}

func (s *featureState) startChildServer() error {
	handler := a2asrv.NewHandler(&deterministicFeatureExecutor{}, a2asrv.WithTaskStore(s.child.store))
	s.server = httptest.NewServer(a2asrv.NewJSONRPCHandler(handler))
	client, err := a2aclient.NewFromEndpoints(context.Background(), []*a2a.AgentInterface{
		a2a.NewAgentInterface(s.server.URL, a2a.TransportProtocolJSONRPC),
	})
	if err != nil {
		s.server.Close()
		s.server = nil
		return fmt.Errorf("feature: create JSON-RPC client: %w", err)
	}
	s.client = client
	return nil
}

func newFeatureCase(kind, principal string) (*featureCase, error) {
	return newFeatureCaseWithAuthenticator(kind, authenticatedAs(principal), testAuthenticator)
}

func newFeatureCaseWithAuthenticator(kind string, ctx context.Context, auth taskstore.Authenticator) (*featureCase, error) {
	root, err := os.MkdirTemp("", "a2a-tape-feature-")
	if err != nil {
		return nil, err
	}
	testCase := &featureCase{kind: kind, root: root, ctx: ctx, auth: auth}
	if err := testCase.open(); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	return testCase, nil
}

func (c *featureCase) open() error {
	backend, err := openFeatureBackend(c.kind, c.root)
	if err != nil {
		return err
	}
	store, err := NewStore(Config{Storage: backend, Authenticator: c.auth})
	if err != nil {
		_ = closeFeatureBackend(backend)
		return err
	}
	c.backend = backend
	c.store = store
	return nil
}

func (c *featureCase) reopen() error {
	if err := closeFeatureBackend(c.backend); err != nil {
		return err
	}
	return c.open()
}

func openFeatureBackend(kind, root string) (storage.TapeStorage, error) {
	switch kind {
	case "jsonl":
		return jsonl.NewJSONLStorage("a2a-feature", filepath.Join(root, "jsonl"))
	case "bbolt":
		return bbolt.NewBboltStorage("a2a-feature", filepath.Join(root, "tape.db"))
	default:
		return nil, fmt.Errorf("feature: unknown storage %q", kind)
	}
}

func closeFeatureBackend(backend storage.TapeStorage) error {
	if backend == nil {
		return nil
	}
	if closer, ok := backend.(interface{ Close() error }); ok {
		return closer.Close()
	}
	return nil
}

func featureTaskState(name string) (a2a.TaskState, error) {
	switch name {
	case "working":
		return a2a.TaskStateWorking, nil
	case "input_required":
		return a2a.TaskStateInputRequired, nil
	case "completed":
		return a2a.TaskStateCompleted, nil
	default:
		return a2a.TaskStateUnspecified, fmt.Errorf("feature: unknown task state %q", name)
	}
}
