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
	Storage       storage.TapeStorage
	Authenticator taskstore.Authenticator
	TimeProvider  func() time.Time
}

type Store struct {
	storage      storage.TapeStorage
	authenticate taskstore.Authenticator
	now          func() time.Time
	mu           sync.Mutex
	initialized  sync.Map
}

type projection struct {
	tasks     map[a2a.TaskID]*taskstore.StoredTask
	recordIDs map[string]taskstore.TaskVersion
}

func NewStore(config Config) (*Store, error) {
	if config.Storage == nil {
		return nil, errors.New("a2a tape: nil storage")
	}
	if config.Authenticator == nil {
		return nil, errors.New("a2a tape: nil authenticator")
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
	s.mu.Lock()
	defer s.mu.Unlock()

	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	state, err := s.replay(ownerCtx)
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
	return s.append(ownerCtx, record)
}

func (s *Store) Get(ctx context.Context, taskID a2a.TaskID) (*taskstore.StoredTask, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ownerCtx, _, err := s.ownerContext(ctx)
	if err != nil {
		return nil, err
	}
	state, err := s.replay(ownerCtx)
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
	s.mu.Lock()
	defer s.mu.Unlock()

	ownerCtx, principal, err := s.ownerContext(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, err
	}
	state, err := s.replay(ownerCtx)
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
	return s.append(ownerCtx, record)
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
		if err := s.storage.Init(ownerCtx); err != nil {
			return nil, "", fmt.Errorf("a2a tape: init owner %q: %w", principal, err)
		}
		s.initialized.Store(principal, struct{}{})
	}
	return ownerCtx, principal, nil
}

func (s *Store) replay(ctx context.Context) (*projection, error) {
	state := &projection{
		tasks:     make(map[a2a.TaskID]*taskstore.StoredTask),
		recordIDs: make(map[string]taskstore.TaskVersion),
	}
	tape, err := s.storage.Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("a2a tape: get tape: %w", err)
	}
	if tape.Scope.SeqE == 0 {
		return state, nil
	}
	entries, err := s.storage.Range(ctx, view.EntryRange{SeqS: 1, SeqE: tape.Scope.SeqE + 1})
	if err != nil {
		return nil, fmt.Errorf("a2a tape: range tape: %w", err)
	}
	for _, tapeEntry := range entries.Raw {
		if !isA2AKind(tapeEntry.GetKind()) {
			continue
		}
		record, err := recordFromEntry(tapeEntry)
		if err != nil {
			return nil, fmt.Errorf("a2a tape: replay seq %d record %s: %w", tapeEntry.GetID(), recordIdentityFromEntry(tapeEntry), err)
		}
		version := taskstore.TaskVersion(tapeEntry.GetID())
		state.recordIDs[record.RecordID] = version
		if record.Task != nil {
			state.tasks[record.TaskID] = &taskstore.StoredTask{Task: record.Task, Version: version}
		}
	}
	return state, nil
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

func (s *Store) append(ctx context.Context, record *tapeRecord) (taskstore.TaskVersion, error) {
	tapeEntry := record.entry()
	tapeEntry.Timestamp = s.now()
	if err := s.storage.Store(ctx, tapeEntry); err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a tape: append record %s: %w", record.RecordID, err)
	}
	tape, err := s.storage.Get(ctx)
	if err != nil {
		return taskstore.TaskVersionMissing, fmt.Errorf("a2a tape: read appended version: %w", err)
	}
	return taskstore.TaskVersion(tape.Scope.SeqE), nil
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
