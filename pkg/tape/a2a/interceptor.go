package a2atape

import (
	"context"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

// PersistenceInterceptor records successful taskless A2A message responses.
type PersistenceInterceptor struct {
	a2asrv.PassthroughCallInterceptor
	store *Store
}

func NewPersistenceInterceptor(store *Store) *PersistenceInterceptor {
	return &PersistenceInterceptor{store: store}
}

func (i *PersistenceInterceptor) After(ctx context.Context, _ *a2asrv.CallContext, response *a2asrv.Response) error {
	if response == nil || response.Err != nil || i == nil || i.store == nil {
		return nil
	}
	message, ok := response.Payload.(*a2a.Message)
	if !ok || message == nil || message.TaskID != "" {
		return nil
	}
	_, err := i.store.persistDirectMessage(ctx, message)
	return err
}
