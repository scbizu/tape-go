package tape

import (
	"context"

	"github.com/scbizu/tape-go/pkg/tape/storage"
)

// Tape is the agent's backend
type Tape struct {
	storage.TapeStorage
}

func (t *Tape) WriteEntry(ctx context.Context) error {
	return nil
}
