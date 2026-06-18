package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/genai"

	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"

	"github.com/scbizu/tape-go/pkg/tape"
	"github.com/scbizu/tape-go/pkg/tape/entry"
	"github.com/scbizu/tape-go/pkg/tape/owner"
	"github.com/scbizu/tape-go/pkg/tape/storage"
	"github.com/scbizu/tape-go/pkg/tape/view"
)

const adkEventExtension = "adk:event"

var (
	_ session.Service = (*TapeAdapter)(nil)
	_ memory.Service  = (*TapeAdapter)(nil)
)

// TapeAdapter exposes a single Tape as ADK session, memory, and context window.
type TapeAdapter struct {
	Tape    *tape.Tape
	AppName string

	state *tapeState
}

func NewTapeAdapter(t *tape.Tape, appName string) (*TapeAdapter, error) {
	if t == nil {
		return nil, errors.New("agent: nil tape")
	}
	if appName == "" {
		return nil, errors.New("agent: empty app name")
	}
	if t.OwnerID == "" {
		return nil, errors.New("agent: empty tape owner")
	}
	return &TapeAdapter{Tape: t, AppName: appName, state: newTapeState(nil)}, nil
}

func (a *TapeAdapter) Create(ctx context.Context, req *session.CreateRequest) (*session.CreateResponse, error) {
	if req == nil {
		return nil, errors.New("agent: nil create session request")
	}
	if err := a.validateIdentity(ctx, req.AppName, req.UserID, req.SessionID); err != nil {
		return nil, err
	}
	a.state.replace(req.State)
	s, err := a.loadSession(ctx, 0, time.Time{})
	if err != nil {
		return nil, err
	}
	return &session.CreateResponse{Session: s}, nil
}

func (a *TapeAdapter) Get(ctx context.Context, req *session.GetRequest) (*session.GetResponse, error) {
	if req == nil {
		return nil, errors.New("agent: nil get session request")
	}
	if req.SessionID == "" {
		return nil, errors.New("agent: empty session ID")
	}
	if err := a.validateIdentity(ctx, req.AppName, req.UserID, req.SessionID); err != nil {
		return nil, err
	}
	s, err := a.loadSession(ctx, req.NumRecentEvents, req.After)
	if err != nil {
		return nil, err
	}
	return &session.GetResponse{Session: s}, nil
}

func (a *TapeAdapter) List(ctx context.Context, req *session.ListRequest) (*session.ListResponse, error) {
	if req == nil {
		return nil, errors.New("agent: nil list sessions request")
	}
	userID := req.UserID
	if userID == "" {
		userID = a.Tape.OwnerID
	}
	if err := a.validateIdentity(ctx, req.AppName, userID, ""); err != nil {
		return nil, err
	}
	s, err := a.loadSession(ctx, 0, time.Time{})
	if err != nil {
		return nil, err
	}
	return &session.ListResponse{Sessions: []session.Session{s}}, nil
}

func (a *TapeAdapter) Delete(context.Context, *session.DeleteRequest) error {
	return errors.New("agent: deleting a tape session is not supported")
}

func (a *TapeAdapter) AppendEvent(ctx context.Context, current session.Session, event *session.Event) error {
	if current == nil || event == nil {
		return errors.New("agent: session and event are required")
	}
	if event.Partial {
		return nil
	}
	if err := a.validateIdentity(ctx, current.AppName(), current.UserID(), current.ID()); err != nil {
		return err
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("agent: encode ADK event: %w", err)
	}
	e := entry.CustomEntry{
		Entry: entry.NewEntry(
			entry.WithEntryKind(eventEntryKind(event)),
			entry.WithEntryContent(eventSummary(event)),
			entry.WithEntryOwner(a.Tape.OwnerID),
		),
		Extensions: map[string]any{adkEventExtension: string(payload)},
	}
	if err := a.Tape.Store(a.tapeContext(ctx), e); err != nil {
		return fmt.Errorf("agent: store ADK event: %w", err)
	}
	a.applyStateDelta(event.Actions.StateDelta)
	return nil
}

func (a *TapeAdapter) AddSessionToMemory(context.Context, session.Session) error {
	return nil
}

func (a *TapeAdapter) SearchMemory(ctx context.Context, req *memory.SearchRequest) (*memory.SearchResponse, error) {
	if req == nil {
		return nil, errors.New("agent: nil search memory request")
	}
	if err := a.validateIdentity(ctx, req.AppName, req.UserID, ""); err != nil {
		return nil, err
	}
	entries, err := a.Tape.Search(a.tapeContext(ctx), storage.WithSemanticPrompt(req.Query))
	if err != nil {
		return nil, err
	}

	result := &memory.SearchResponse{Memories: make([]memory.Entry, 0, len(entries.Raw))}
	for _, tapeEntry := range entries.Raw {
		event, err := eventFromEntry(tapeEntry)
		if err != nil {
			return nil, err
		}
		if event.Content == nil {
			continue
		}
		result.Memories = append(result.Memories, memory.Entry{
			ID:             event.ID,
			Content:        event.Content,
			Author:         event.Author,
			Timestamp:      event.Timestamp,
			CustomMetadata: event.CustomMetadata,
		})
	}
	return result, nil
}

// ContextWindow replaces ADK's model contents with the Tape's active view.
func (a *TapeAdapter) ContextWindow(ctx adkagent.CallbackContext, req *model.LLMRequest) (*model.LLMResponse, error) {
	tapeView, err := a.Tape.Get(a.tapeContext(ctx))
	if err != nil {
		return nil, err
	}
	entries, err := a.activeEntries(ctx, tapeView)
	if err != nil {
		return nil, err
	}
	contents := make([]*genai.Content, 0, len(entries))
	for _, tapeEntry := range entries {
		if strings.HasPrefix(string(tapeEntry.GetKind()), "anchor:") {
			continue
		}
		event, err := eventFromEntry(tapeEntry)
		if err != nil {
			return nil, err
		}
		if event.Content != nil {
			contents = append(contents, event.Content)
		}
	}
	req.Contents = contents
	return nil, nil
}

func (a *TapeAdapter) loadSession(ctx context.Context, recent int, after time.Time) (*tapeSession, error) {
	tapeView, err := a.Tape.Get(a.tapeContext(ctx))
	if err != nil {
		return nil, err
	}
	entries, err := a.activeEntries(ctx, tapeView)
	if err != nil {
		return nil, err
	}
	events := make(tapeEvents, 0, len(entries))
	for _, tapeEntry := range entries {
		if strings.HasPrefix(string(tapeEntry.GetKind()), "anchor:") {
			continue
		}
		event, err := eventFromEntry(tapeEntry)
		if err != nil {
			return nil, err
		}
		a.applyStateDelta(event.Actions.StateDelta)
		if !after.IsZero() && event.Timestamp.Before(after) {
			continue
		}
		events = append(events, event)
	}
	if recent > 0 && len(events) > recent {
		events = events[len(events)-recent:]
	}
	updatedAt := time.Time{}
	if len(events) > 0 {
		updatedAt = events[len(events)-1].Timestamp
	}
	return &tapeSession{
		id:        tapeView.SessionID,
		appName:   a.AppName,
		userID:    a.Tape.OwnerID,
		state:     a.state,
		events:    events,
		updatedAt: updatedAt,
	}, nil
}

func (a *TapeAdapter) activeEntries(ctx context.Context, tapeView view.TapeView) ([]entry.EntryLike, error) {
	if tapeView.Scope.SeqE == 0 {
		return nil, nil
	}
	start := a.Tape.View.SeqS
	if start == 0 {
		start = 1
	}
	entries, err := a.Tape.Range(a.tapeContext(ctx), view.EntryRange{
		SeqS: start,
		SeqE: entry.NextEntryID(tapeView.Scope.SeqE),
	})
	if err != nil {
		return nil, err
	}
	return entries.Raw, nil
}

func (a *TapeAdapter) validateIdentity(ctx context.Context, appName, userID, sessionID string) error {
	if appName != a.AppName {
		return fmt.Errorf("agent: app name %q does not match %q", appName, a.AppName)
	}
	if userID != a.Tape.OwnerID {
		return fmt.Errorf("agent: user ID %q does not match tape owner %q", userID, a.Tape.OwnerID)
	}
	tapeView, err := a.Tape.Get(a.tapeContext(ctx))
	if err != nil {
		return err
	}
	if sessionID != "" && sessionID != tapeView.SessionID {
		return fmt.Errorf("agent: session ID %q does not match tape session %q", sessionID, tapeView.SessionID)
	}
	return nil
}

func (a *TapeAdapter) tapeContext(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return owner.WithOwnerId(ctx, a.Tape.OwnerID)
}

func (a *TapeAdapter) applyStateDelta(delta map[string]any) {
	for key, value := range delta {
		if !strings.HasPrefix(key, session.KeyPrefixTemp) {
			_ = a.state.Set(key, value)
		}
	}
}

type tapeSession struct {
	id        string
	appName   string
	userID    string
	state     *tapeState
	events    tapeEvents
	updatedAt time.Time
}

func (s *tapeSession) ID() string                { return s.id }
func (s *tapeSession) AppName() string           { return s.appName }
func (s *tapeSession) UserID() string            { return s.userID }
func (s *tapeSession) State() session.State      { return s.state }
func (s *tapeSession) Events() session.Events    { return s.events }
func (s *tapeSession) LastUpdateTime() time.Time { return s.updatedAt }

type tapeEvents []*session.Event

func (events tapeEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range events {
			if !yield(event) {
				return
			}
		}
	}
}

func (events tapeEvents) Len() int                    { return len(events) }
func (events tapeEvents) At(index int) *session.Event { return events[index] }

type tapeState struct {
	mu     sync.RWMutex
	values map[string]any
}

func newTapeState(values map[string]any) *tapeState {
	return &tapeState{values: maps.Clone(values)}
}

func (s *tapeState) Get(key string) (any, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.values[key]
	if !ok {
		return nil, session.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *tapeState) Set(key string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = value
	return nil
}

func (s *tapeState) All() iter.Seq2[string, any] {
	s.mu.RLock()
	values := maps.Clone(s.values)
	s.mu.RUnlock()
	return maps.All(values)
}

func (s *tapeState) replace(values map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.values = maps.Clone(values)
}

func eventFromEntry(tapeEntry entry.EntryLike) (*session.Event, error) {
	if raw, ok := eventExtension(tapeEntry); ok {
		var event session.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("agent: decode ADK event from entry %d: %w", tapeEntry.GetID(), err)
		}
		return &event, nil
	}

	role := genai.RoleModel
	author := "agent"
	if tapeEntry.GetKind() == entry.EntryUser {
		role = genai.RoleUser
		author = "user"
	}
	return &session.Event{
		ID:     strconv.FormatUint(tapeEntry.GetID(), 10),
		Author: author,
		LLMResponse: model.LLMResponse{Content: &genai.Content{
			Role:  role,
			Parts: []*genai.Part{{Text: tapeEntry.GetSummary()}},
		}},
	}, nil
}

func eventExtension(tapeEntry entry.EntryLike) (string, bool) {
	var extensions map[string]any
	switch e := tapeEntry.(type) {
	case entry.CustomEntry:
		extensions = e.Extensions
	case *entry.CustomEntry:
		extensions = e.Extensions
	default:
		return "", false
	}
	raw, ok := extensions[adkEventExtension].(string)
	return raw, ok
}

func eventEntryKind(event *session.Event) entry.EntryKind {
	if event.Author == "user" || event.Content != nil && event.Content.Role == genai.RoleUser {
		return entry.EntryUser
	}
	for _, part := range eventParts(event) {
		if part.FunctionResponse != nil {
			return entry.EntryToolResult
		}
		if part.FunctionCall != nil {
			return entry.EntryToolCall
		}
	}
	return entry.EntryAssistant
}

func eventSummary(event *session.Event) string {
	var summary strings.Builder
	for _, part := range eventParts(event) {
		summary.WriteString(part.Text)
	}
	if summary.Len() == 0 {
		summary.WriteString(event.ErrorMessage)
	}
	return summary.String()
}

func eventParts(event *session.Event) []*genai.Part {
	if event.Content == nil {
		return nil
	}
	return event.Content.Parts
}
