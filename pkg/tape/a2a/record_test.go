package a2atape

import (
	"reflect"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

func testTask(id a2a.TaskID, contextID string, state a2a.TaskState) *a2a.Task {
	now := time.Date(2026, 8, 3, 7, 0, 0, 0, time.UTC)
	return &a2a.Task{
		ID:        id,
		ContextID: contextID,
		Status: a2a.TaskStatus{
			State:     state,
			Timestamp: &now,
		},
	}
}

func TestRecordRoundTrip(t *testing.T) {
	task := testTask("task-1", "context-1", a2a.TaskStateWorking)
	event := a2a.NewStatusUpdateEvent(task, a2a.TaskStateWorking, nil)
	event.Status.Timestamp = task.Status.Timestamp

	record, err := newTaskRecord("owner-a", task, event, taskstore.TaskVersion(1))
	if err != nil {
		t.Fatalf("newTaskRecord() error = %v", err)
	}
	got, err := recordFromEntry(record.entry())
	if err != nil {
		t.Fatalf("recordFromEntry() error = %v", err)
	}

	if got.ProfileVersion != profileVersion {
		t.Fatalf("ProfileVersion = %d, want %d", got.ProfileVersion, profileVersion)
	}
	if got.A2AVersion != string(a2a.Version) {
		t.Fatalf("A2AVersion = %q, want %q", got.A2AVersion, a2a.Version)
	}
	if got.Owner != "owner-a" || got.TaskID != task.ID || got.ContextID != task.ContextID {
		t.Fatalf("searchable identity = (%q, %q, %q)", got.Owner, got.TaskID, got.ContextID)
	}
	if got.Kind != kindStatusUpdate {
		t.Fatalf("Kind = %q, want %q", got.Kind, kindStatusUpdate)
	}
	if !reflect.DeepEqual(got.Task, task) {
		t.Fatalf("Task round trip mismatch\n got: %#v\nwant: %#v", got.Task, task)
	}
	if !reflect.DeepEqual(got.Event.Event, event) {
		t.Fatalf("Event round trip mismatch\n got: %#v\nwant: %#v", got.Event.Event, event)
	}
	if got.RecordID == "" {
		t.Fatal("RecordID is empty")
	}
}

func TestRecordRejectsMismatchedEventIdentity(t *testing.T) {
	task := testTask("task-1", "context-1", a2a.TaskStateWorking)
	event := a2a.NewStatusUpdateEvent(task, a2a.TaskStateWorking, nil)
	event.TaskID = "other-task"

	if _, err := newTaskRecord("owner-a", task, event, 1); err == nil {
		t.Fatal("newTaskRecord() error = nil, want identity mismatch")
	}
}

func TestRecordRejectsMissingSearchableMessageID(t *testing.T) {
	task := testTask("task-1", "context-1", a2a.TaskStateWorking)
	message := &a2a.Message{ID: "message-1", TaskID: task.ID, ContextID: task.ContextID}
	record, err := newTaskRecord("owner-a", task, message, 1)
	if err != nil {
		t.Fatal(err)
	}
	record.MessageID = ""

	if _, err := recordFromEntry(record.entry()); err == nil {
		t.Fatal("recordFromEntry() error = nil, want required messageId validation")
	}
}

func TestDirectMessageRecordRoundTrip(t *testing.T) {
	message := &a2a.Message{ID: "message-1", ContextID: "context-1", Role: a2a.MessageRoleAgent}
	record, err := newMessageRecord("owner-a", message)
	if err != nil {
		t.Fatalf("newMessageRecord() error = %v", err)
	}
	got, err := recordFromEntry(record.entry())
	if err != nil {
		t.Fatalf("recordFromEntry() error = %v", err)
	}
	if got.Kind != kindMessage || got.MessageID != message.ID {
		t.Fatalf("direct message identity = (%q, %q), want (%q, %q)", got.Kind, got.MessageID, kindMessage, message.ID)
	}
	if got.Task != nil || got.TaskID != "" {
		t.Fatalf("direct record unexpectedly contains a task: %#v", got)
	}
	if !reflect.DeepEqual(got.Message, message) || !reflect.DeepEqual(got.Event.Event, message) {
		t.Fatalf("direct message round trip mismatch: %#v", got)
	}
}
