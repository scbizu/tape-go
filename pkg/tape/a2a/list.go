package a2atape

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv/taskstore"
)

const defaultPageSize = 50
const defaultMaxHistoryLength = 100

type listItem struct {
	stored    *taskstore.StoredTask
	updatedAt time.Time
}

type pageCursor struct {
	UpdatedAt time.Time  `json:"updatedAt"`
	TaskID    a2a.TaskID `json:"taskId"`
}

func (s *Store) List(ctx context.Context, req *a2a.ListTasksRequest) (*a2a.ListTasksResponse, error) {
	if req == nil {
		req = &a2a.ListTasksRequest{}
	}
	if err := validateValue("list page size", req.PageSize, "omitempty,min=1,max=100"); err != nil {
		return nil, fmt.Errorf("%w: %w", err, a2a.ErrInvalidRequest)
	}
	pageSize := req.PageSize
	if pageSize == 0 {
		pageSize = defaultPageSize
	}
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
	items := make([]listItem, 0, len(state.tasks))
	for taskID, stored := range state.tasks {
		if req.ContextID != "" && stored.Task.ContextID != req.ContextID {
			continue
		}
		if req.Status != a2a.TaskStateUnspecified && stored.Task.Status.State != req.Status {
			continue
		}
		if req.StatusTimestampAfter != nil && stored.Task.Status.Timestamp != nil && stored.Task.Status.Timestamp.Before(*req.StatusTimestampAfter) {
			continue
		}
		items = append(items, listItem{stored: stored, updatedAt: state.updatedAt[taskID]})
	}
	slices.SortFunc(items, compareListItems)
	totalSize := len(items)
	if req.PageToken != "" {
		cursor, err := decodePageCursor(req.PageToken)
		if err != nil {
			return nil, err
		}
		start := len(items)
		for i, item := range items {
			if item.updatedAt.Before(cursor.UpdatedAt) || (item.updatedAt.Equal(cursor.UpdatedAt) && string(item.stored.Task.ID) < string(cursor.TaskID)) {
				start = i
				break
			}
		}
		items = items[start:]
	}
	nextPageToken := ""
	if len(items) > pageSize {
		last := items[pageSize-1]
		nextPageToken, err = encodePageCursor(last.updatedAt, last.stored.Task.ID)
		if err != nil {
			return nil, err
		}
		items = items[:pageSize]
	}
	tasks := make([]*a2a.Task, 0, len(items))
	for _, item := range items {
		task, err := cloneTask(item.stored.Task)
		if err != nil {
			return nil, err
		}
		historyLength := defaultMaxHistoryLength
		if req.HistoryLength != nil {
			historyLength = *req.HistoryLength
		}
		if historyLength == 0 {
			task.History = []*a2a.Message{}
		} else if historyLength > 0 && len(task.History) > historyLength {
			task.History = task.History[len(task.History)-historyLength:]
		}
		if !req.IncludeArtifacts {
			task.Artifacts = nil
		}
		tasks = append(tasks, task)
	}
	return &a2a.ListTasksResponse{
		Tasks:         tasks,
		TotalSize:     totalSize,
		PageSize:      pageSize,
		NextPageToken: nextPageToken,
	}, nil
}

func compareListItems(a, b listItem) int {
	if compared := b.updatedAt.Compare(a.updatedAt); compared != 0 {
		return compared
	}
	return strings.Compare(string(b.stored.Task.ID), string(a.stored.Task.ID))
}

func encodePageCursor(updatedAt time.Time, taskID a2a.TaskID) (string, error) {
	payload, err := json.Marshal(pageCursor{UpdatedAt: updatedAt, TaskID: taskID})
	if err != nil {
		return "", fmt.Errorf("a2a tape: encode page token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodePageCursor(token string) (pageCursor, error) {
	payload, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return pageCursor{}, fmt.Errorf("a2a tape: decode page token: %w", a2a.ErrParseError)
	}
	var cursor pageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil || cursor.UpdatedAt.IsZero() || cursor.TaskID == "" {
		return pageCursor{}, fmt.Errorf("a2a tape: decode page token: %w", a2a.ErrParseError)
	}
	return cursor, nil
}
