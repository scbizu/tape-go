package a2atape

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

type Config struct {
	Storage       storage.TapeStorage     `validate:"required"`
	Authenticator taskstore.Authenticator `validate:"required"`
	TimeProvider  func() time.Time
}

type Store struct {
	storage      storage.TapeStorage
	authenticate taskstore.Authenticator
	now          func() time.Time
	initMu       sync.Mutex
	initialized  sync.Map
	owners       sync.Map
}

type ownerProjection struct {
	mu             sync.Mutex
	projection     *projection
	lastAppliedSeq entry.Seq
	loaded         bool
}

type projection struct {
	tasks     map[a2a.TaskID]*taskstore.StoredTask
	recordIDs map[string]taskstore.TaskVersion
	updatedAt map[a2a.TaskID]time.Time
}

const maxTaskVersion = taskstore.TaskVersion(1<<63 - 1)

func NewStore(config Config) (*Store, error) {
	if err := validateStructure("config", config); err != nil {
		return nil, err
	}
	if config.TimeProvider == nil {
		config.TimeProvider = time.Now
	}
	return &Store{
		storage:      config.Storage,
		authenticate: config.Authenticator,
		now:          config.TimeProvider,
	}, nil
}

func (s *Store) Create(ctx context.Context, task *a2a.Task) (taskstore.TaskVersion, error) {
	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	ownerState := s.ownerProjection(principal)
	ownerState.mu.Lock()
	defer ownerState.mu.Unlock()

	state, err := s.syncProjection(ownerCtx, ownerState)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	if task == nil {
		return taskstore.TaskVersionMissing, errors.New("a2a tape: nil task")
	}
	if _, exists := state.tasks[task.ID]; exists {
		return taskstore.TaskVersionMissing, taskstore.ErrTaskAlreadyExists
	}
	record, err := newTaskRecord(principal, task, task, taskstore.TaskVersionMissing)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	return s.append(ownerCtx, ownerState, record)
}

func (s *Store) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return nil, err
	}
	ownerState := s.ownerProjection(principal)
	ownerState.mu.Lock()
	defer ownerState.mu.Unlock()

	state, err := s.syncProjection(ownerCtx, ownerState)
	if err != nil {
		return nil, err
	}
	stored, exists := state.tasks[taskID]
	if !exists {
		return nil, a2a.ErrTaskNotFound
	}
	cloned, err := cloneTask(stored.Task)
	if err != nil {
		return nil, err
	}
	return &taskstore.StoredTask{Task: cloned, Version: stored.Version}, nil
}

func (s *Store) Update(ctx context.Context, update *taskstore.UpdateRequest) (taskstore.TaskVersion, error) {
	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	ownerState := s.ownerProjection(principal)
	ownerState.mu.Lock()
	defer ownerState.mu.Unlock()

	state, err := s.syncProjection(ownerCtx, ownerState)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	if update == nil || update.Task == nil || update.Event == nil {
		return taskstore.TaskVersionMissing, errors.New("a2a tape: incomplete update request")
	}
	record, err := newTaskRecord(principal, update.Task, update.Event, update.PrevVersion)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	if version, exists := state.recordIDs[record.RecordID]; exists {
		return version, nil
	}
	stored, exists := state.tasks[update.Task.ID]
	if !exists {
		return taskstore.TaskVersionMissing, a2a.ErrTaskNotFound
	}
	if update.PrevVersion != taskstore.TaskVersionMissing && update.PrevVersion != stored.Version {
		return taskstore.TaskVersionMissing, taskstore.ErrConcurrentModification
	}
	if _, err := nextTaskVersion(stored.Version); err != nil {
		return taskstore.TaskVersionMissing, err
	}
	return s.append(ownerCtx, ownerState, record)
}

func (s *Store) persistDirectMessage(ctx context.Context, message *a2a.Message) (taskstore.TaskVersion, error) {
	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	ownerState := s.ownerProjection(principal)
	ownerState.mu.Lock()
	defer ownerState.mu.Unlock()

	state, err := s.syncProjection(ownerCtx, ownerState)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	record, err := newMessageRecord(principal, message)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	if version, exists := state.recordIDs[record.RecordID]; exists {
		return version, nil
	}
	return s.append(ownerCtx, ownerState, record)
}

func (s *Store) ownerContext(ctx context.Context) (context.Context, string, error) {
	principal, err := s.authenticate(ctx)
	if err != nil {
		return nil, "", err
	}
	if principal == "" {
		return nil, "", fmt.Errorf("a2a tape: empty authenticated principal: %w", a2a.ErrUnauthenticated)
	}
	ownerCtx := owner.WithOwnerId(ctx, principal)
	if _, ok := s.initialized.Load(principal); !ok {
		s.initMu.Lock()
		defer s.initMu.Unlock()
		if _, ok := s.initialized.Load(principal); !ok {
			if err := s.storage.Init(ownerCtx); err != nil {
				return nil, "", fmt.Errorf("a2a tape: init owner %q: %w", principal, err)
			}
			s.initialized.Store(principal, struct{}{})
		}
	}
	return ownerCtx, principal, nil
}

func (s *Store) ownerProjection(principal string) *ownerProjection {
	value, _ := s.owners.LoadOrStore(principal, &ownerProjection{})
	return value.(*ownerProjection)
}

func newProjection() *projection {
	return &projection{
		tasks:     make(map[a2a.TaskID]*taskstore.StoredTask),
		recordIDs: make(map[string]taskstore.TaskVersion),
		updatedAt: make(map[a2a.TaskID]time.Time),
	}
}

func (s *Store) syncProjection(ctx context.Context, cached *ownerProjection) (*projection, error) {
	tape, err := s.storage.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("a2a tape: get tape: %w", err)
	}
	head := tape.Scope.SeqE
	if cached.loaded && head.Cmp(cached.lastAppliedSeq) < 0 {
		lastApplied := cached.lastAppliedSeq
		cached.projection = nil
		cached.lastAppliedSeq = entry.Seq{}
		cached.loaded = false
		return nil, fmt.Errorf("a2a tape: head regressed from seq %s to %s", lastApplied, head)
	}
	if cached.loaded && head == cached.lastAppliedSeq {
		return cached.projection, nil
	}

	start := entry.SeqFromUint64(1)
	state := newProjection()
	if cached.loaded {
		start = cached.lastAppliedSeq.Next()
		state = cloneProjection(cached.projection)
	}
	if head.IsZero() {
		cached.projection = state
		cached.lastAppliedSeq = entry.Seq{}
		cached.loaded = true
		return state, nil
	}
	entries, err := s.storage.Range(ctx, view.EntryRange{SeqS: start, SeqE: head.Next()})
	if err != nil {
		return nil, fmt.Errorf("a2a tape: range tape: %w", err)
	}
	if err := validateReplayRange(entries.Raw, start, head); err != nil {
		return nil, err
	}

	type decodedRecord struct {
		record  *tapeRecord
		updated time.Time
	}
	decoded := make([]decodedRecord, 0, len(entries.Raw))
	for _, tapeEntry := range entries.Raw {
		if !isA2AKind(tapeEntry.GetKind()) {
			continue
		}
		record, err := recordFromEntry(tapeEntry)
		if err != nil {
			return nil, fmt.Errorf("a2a tape: replay seq %s record %s: %w", tapeEntry.GetID(), recordIdentityFromEntry(tapeEntry), err)
		}
		decoded = append(decoded, decodedRecord{
			record:  record,
			updated: tapeEntry.GetTimestamp(),
		})
	}
	for _, item := range decoded {
		if err := applyRecord(state, item.record, item.updated); err != nil {
			return nil, fmt.Errorf("a2a tape: replay record %s: %w", item.record.RecordID, err)
		}
	}
	cached.projection = state
	cached.lastAppliedSeq = head
	cached.loaded = true
	return state, nil
}

func validateReplayRange(entries []entry.EntryLike, start, head entry.Seq) error {
	if len(entries) == 0 {
		return fmt.Errorf("a2a tape: replay range [%s,%s] returned no entries", start, head)
	}
	expected := start
	for _, tapeEntry := range entries {
		if tapeEntry.GetID() != expected {
			return fmt.Errorf("a2a tape: replay expected seq %s, got %s", expected, tapeEntry.GetID())
		}
		expected = expected.Next()
	}
	if expected != head.Next() {
		return fmt.Errorf("a2a tape: replay ended before seq %s", head)
	}
	return nil
}

func nextTaskVersion(current taskstore.TaskVersion) (taskstore.TaskVersion, error) {
	if current == maxTaskVersion {
		return taskstore.TaskVersionMissing, errors.New("a2a tape: task version exhausted")
	}
	return current + 1, nil
}

func applyRecord(state *projection, record *tapeRecord, updated time.Time) error {
	version := taskstore.TaskVersionMissing
	if record.Task != nil {
		previous, exists := state.tasks[record.TaskID]
		if !exists {
			if record.PrevVersion != taskstore.TaskVersionMissing {
				return errors.New("first task record has a previous version")
			}
			version = 1
		} else {
			if record.PrevVersion != taskstore.TaskVersionMissing && record.PrevVersion != previous.Version {
				return fmt.Errorf("previous version is %d, want %d", record.PrevVersion, previous.Version)
			}
			var err error
			version, err = nextTaskVersion(previous.Version)
			if err != nil {
				return err
			}
		}
		state.tasks[record.TaskID] = &taskstore.StoredTask{Task: record.Task, Version: version}
		state.updatedAt[record.TaskID] = updated
	}
	if _, exists := state.recordIDs[record.RecordID]; !exists {
		state.recordIDs[record.RecordID] = version
	}
	return nil
}

func cloneProjection(source *projection) *projection {
	cloned := newProjection()
	for id, task := range source.tasks {
		cloned.tasks[id] = task
	}
	for id, version := range source.recordIDs {
		cloned.recordIDs[id] = version
	}
	for id, updated := range source.updatedAt {
		cloned.updatedAt[id] = updated
	}
	return cloned
}

func recordIdentityFromEntry(e entry.EntryLike) string {
	var extensions map[string]any
	switch custom := e.(type) {
	case entry.CustomEntry:
		extensions = custom.Extensions
	case *entry.CustomEntry:
		if custom != nil {
			extensions = custom.Extensions
		}
	}
	raw, ok := extensions[recordExtension]
	if !ok {
		return "<unknown>"
	}
	var payload []byte
	switch value := raw.(type) {
	case string:
		payload = []byte(value)
	case json.RawMessage:
		payload = value
	case []byte:
		payload = value
	default:
		return "<unknown>"
	}
	var identity struct {
		RecordID string `json:"recordId"`
	}
	if err := json.Unmarshal(payload, &identity); err != nil || identity.RecordID == "" {
		return "<unknown>"
	}
	return identity.RecordID
}

func isA2AKind(kind entry.EntryKind) bool {
	switch string(kind) {
	case string(kindTask), string(kindMessage), string(kindStatusUpdate), string(kindArtifactUpdate):
		return true
	default:
		return false
	}
}

func (s *Store) append(ctx context.Context, cached *ownerProjection, record *tapeRecord) (taskstore.TaskVersion, error) {
	tapeEntry := record.entry()
	tapeEntry.Timestamp = s.now()
	if err := s.storage.Store(ctx, tapeEntry); err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a tape: append record %s: %w", record.RecordID, err)
	}
	state, err := s.syncProjection(ctx, cached)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a tape: replay appended record %s: %w", record.RecordID, err)
	}
	version, exists := state.recordIDs[record.RecordID]
	if !exists {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a tape: appended record %s is missing from replay", record.RecordID)
	}
	return version, nil
}

func cloneTask(task *a2a.Task) (*a2a.Task, error) {
	payload, err := json.Marshal(task)
	if err != nil {
		return nil, fmt.Errorf("a2a tape: clone task: %w", err)
	}
	var cloned a2a.Task
	if err := json.Unmarshal(payload, &cloned); err != nil {
		return nil, fmt.Errorf("a2a tape: clone task: %w", err)
	}
	return &cloned, nil
}
