package a2atape

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
	"github.com/scbizu/tape-go/pkg/tape/entry"
)

const (
	profileVersion  = 1
	recordExtension = "a2a:record"

	kindTask           entry.EntryKind = "a2a:task"
	kindMessage        entry.EntryKind = "a2a:message"
	kindStatusUpdate   entry.EntryKind = "a2a:status_update"
	kindArtifactUpdate entry.EntryKind = "a2a:artifact_update"
)

type tapeRecord struct {
	ProfileVersion int                   `json:"profileVersion" validate:"eq=1"`
	A2AVersion     string                `json:"a2aVersion" validate:"eq=1.0"`
	RecordID       string                `json:"recordId" validate:"required,len=64,hexadecimal"`
	Owner          string                `json:"owner" validate:"required"`
	Kind           entry.EntryKind       `json:"kind" validate:"required,oneof=a2a:task a2a:message a2a:status_update a2a:artifact_update"`
	TaskID         a2a.TaskID            `json:"taskId,omitempty" validate:"required_without=Message"`
	ContextID      string                `json:"contextId,omitempty" validate:"required_without=Message"`
	MessageID      string                `json:"messageId,omitempty" validate:"required_if=Kind a2a:message"`
	Task           *a2a.Task             `json:"task,omitempty" validate:"required_without=Message"`
	Message        *a2a.Message          `json:"message,omitempty"`
	Event          *a2a.StreamResponse   `json:"event,omitempty" validate:"required"`
	PrevVersion    taskstore.TaskVersion `json:"prevVersion,omitempty" validate:"gte=0"`
}

type taskRecordInput struct {
	Owner string    `validate:"required"`
	Task  *a2a.Task `validate:"required"`
	Event a2a.Event `validate:"required"`
}

func newTaskRecord(owner string, task *a2a.Task, event a2a.Event, prevVersion taskstore.TaskVersion) (*tapeRecord, error) {
	if err := validateStructure("task record input", taskRecordInput{Owner: owner, Task: task, Event: event}); err != nil {
		return nil, err
	}
	if task.ID == "" || task.ContextID == "" {
		return nil, errors.New("a2a tape: task identity is incomplete")
	}

	kind, err := kindForEvent(event)
	if err != nil {
		return nil, err
	}
	info := event.TaskInfo()
	if info.TaskID != task.ID || info.ContextID != task.ContextID {
		return nil, fmt.Errorf("a2a tape: event identity (%q, %q) does not match task (%q, %q)", info.TaskID, info.ContextID, task.ID, task.ContextID)
	}

	record := &tapeRecord{
		ProfileVersion: profileVersion,
		A2AVersion:     string(a2a.Version),
		Owner:          owner,
		Kind:           kind,
		TaskID:         task.ID,
		ContextID:      task.ContextID,
		Task:           task,
		Event:          &a2a.StreamResponse{Event: event},
		PrevVersion:    prevVersion,
	}
	if message, ok := event.(*a2a.Message); ok {
		record.MessageID = message.ID
	}
	record.RecordID, err = recordID(record)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func newMessageRecord(owner string, message *a2a.Message) (*tapeRecord, error) {
	if owner == "" {
		return nil, errors.New("a2a tape: empty owner")
	}
	if message == nil || message.ID == "" {
		return nil, errors.New("a2a tape: direct message identity is incomplete")
	}
	if message.TaskID != "" {
		return nil, errors.New("a2a tape: direct message must not reference a task")
	}
	record := &tapeRecord{
		ProfileVersion: profileVersion,
		A2AVersion:     string(a2a.Version),
		Owner:          owner,
		Kind:           kindMessage,
		ContextID:      message.ContextID,
		MessageID:      message.ID,
		Message:        message,
		Event:          &a2a.StreamResponse{Event: message},
	}
	var err error
	record.RecordID, err = recordID(record)
	if err != nil {
		return nil, err
	}
	return record, nil
}

func kindForEvent(event a2a.Event) (entry.EntryKind, error) {
	switch event.(type) {
	case *a2a.Task:
		return kindTask, nil
	case *a2a.Message:
		return kindMessage, nil
	case *a2a.TaskStatusUpdateEvent:
		return kindStatusUpdate, nil
	case *a2a.TaskArtifactUpdateEvent:
		return kindArtifactUpdate, nil
	default:
		return "", fmt.Errorf("a2a tape: unsupported event type %T", event)
	}
}

func (r *tapeRecord) entry() entry.CustomEntry {
	payload, err := json.Marshal(r)
	if err != nil {
		panic(fmt.Sprintf("a2a tape: marshal validated record: %v", err))
	}
	content := string(r.TaskID)
	if r.Kind == kindMessage {
		content = r.MessageID
	}
	return entry.CustomEntry{
		Entry: entry.NewEntry(
			entry.WithEntryKind(r.Kind),
			entry.WithEntryOwner(r.Owner),
			entry.WithEntryContent(content),
		),
		Extensions: map[string]any{recordExtension: string(payload)},
	}
}

func recordFromEntry(e entry.EntryLike) (*tapeRecord, error) {
	var custom entry.CustomEntry
	switch v := e.(type) {
	case entry.CustomEntry:
		custom = v
	case *entry.CustomEntry:
		if v == nil {
			return nil, errors.New("a2a tape: nil entry")
		}
		custom = *v
	default:
		return nil, fmt.Errorf("a2a tape: entry kind %q has no custom record", e.GetKind())
	}

	raw, ok := custom.Extensions[recordExtension]
	if !ok {
		return nil, errors.New("a2a tape: missing a2a:record extension")
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
		return nil, fmt.Errorf("a2a tape: a2a:record extension has type %T", raw)
	}

	var record tapeRecord
	if err := json.Unmarshal(payload, &record); err != nil {
		return nil, fmt.Errorf("a2a tape: decode record: %w", err)
	}
	if err := record.validate(custom); err != nil {
		return nil, err
	}
	return &record, nil
}

func (r *tapeRecord) validate(e entry.CustomEntry) error {
	if err := validateStructure("record", r); err != nil {
		return err
	}
	if r.ProfileVersion != profileVersion {
		return fmt.Errorf("a2a tape: unsupported profile version %d", r.ProfileVersion)
	}
	if r.A2AVersion != string(a2a.Version) {
		return fmt.Errorf("a2a tape: unsupported A2A version %q", r.A2AVersion)
	}
	if r.Owner == "" || r.Owner != e.GetOwner() {
		return fmt.Errorf("a2a tape: record owner %q does not match entry owner %q", r.Owner, e.GetOwner())
	}
	if r.Kind != e.GetKind() {
		return fmt.Errorf("a2a tape: record kind %q does not match entry kind %q", r.Kind, e.GetKind())
	}
	if r.Event == nil || r.Event.Event == nil {
		return errors.New("a2a tape: task record is incomplete")
	}
	if r.Kind == kindMessage && r.Message != nil {
		return r.validateDirectMessage()
	}
	if r.Task == nil {
		return errors.New("a2a tape: task record is incomplete")
	}
	if r.TaskID != r.Task.ID || r.ContextID != r.Task.ContextID {
		return errors.New("a2a tape: searchable identity does not match task")
	}
	info := r.Event.Event.TaskInfo()
	if info.TaskID != r.TaskID || info.ContextID != r.ContextID {
		return errors.New("a2a tape: event identity does not match task")
	}
	kind, err := kindForEvent(r.Event.Event)
	if err != nil {
		return err
	}
	if kind != r.Kind {
		return fmt.Errorf("a2a tape: event kind %q does not match record kind %q", kind, r.Kind)
	}
	wantID, err := recordID(r)
	if err != nil {
		return err
	}
	if r.RecordID == "" || r.RecordID != wantID {
		return errors.New("a2a tape: record ID mismatch")
	}
	return nil
}

func (r *tapeRecord) validateDirectMessage() error {
	if r.Task != nil || r.TaskID != "" {
		return errors.New("a2a tape: direct message must not contain a task")
	}
	if r.MessageID == "" || r.Message.ID != r.MessageID {
		return errors.New("a2a tape: searchable message identity does not match message")
	}
	if r.Message.TaskID != "" || r.ContextID != r.Message.ContextID {
		return errors.New("a2a tape: direct message identity is inconsistent")
	}
	eventMessage, ok := r.Event.Event.(*a2a.Message)
	if !ok {
		return fmt.Errorf("a2a tape: direct message event has type %T", r.Event.Event)
	}
	eventJSON, err := json.Marshal(eventMessage)
	if err != nil {
		return fmt.Errorf("a2a tape: marshal direct message event: %w", err)
	}
	messageJSON, err := json.Marshal(r.Message)
	if err != nil {
		return fmt.Errorf("a2a tape: marshal direct message: %w", err)
	}
	if string(eventJSON) != string(messageJSON) {
		return errors.New("a2a tape: direct message and event differ")
	}
	wantID, err := recordID(r)
	if err != nil {
		return err
	}
	if r.RecordID == "" || r.RecordID != wantID {
		return errors.New("a2a tape: record ID mismatch")
	}
	return nil
}

func recordID(r *tapeRecord) (string, error) {
	taskJSON, err := json.Marshal(r.Task)
	if err != nil {
		return "", fmt.Errorf("a2a tape: marshal task: %w", err)
	}
	messageJSON, err := json.Marshal(r.Message)
	if err != nil {
		return "", fmt.Errorf("a2a tape: marshal message: %w", err)
	}
	eventJSON, err := json.Marshal(r.Event)
	if err != nil {
		return "", fmt.Errorf("a2a tape: marshal event: %w", err)
	}
	payload, err := json.Marshal(struct {
		Owner       string                `json:"owner"`
		Kind        entry.EntryKind       `json:"kind"`
		PrevVersion taskstore.TaskVersion `json:"prevVersion"`
		Task        json.RawMessage       `json:"task"`
		Message     json.RawMessage       `json:"message"`
		Event       json.RawMessage       `json:"event"`
	}{
		Owner:       r.Owner,
		Kind:        r.Kind,
		PrevVersion: r.PrevVersion,
		Task:        taskJSON,
		Message:     messageJSON,
		Event:       eventJSON,
	})
	if err != nil {
		return "", fmt.Errorf("a2a tape: marshal record identity: %w", err)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:]), nil
}
