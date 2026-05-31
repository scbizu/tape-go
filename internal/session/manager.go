package session

import (
	"errors"
	"sync"

	"github.com/scbizu/tape-go/internal/tape"
)

var (
	ErrActiveSessionExists = errors.New("active session already exists")
	ErrSessionNotFound     = errors.New("session not found")
)

type Session struct {
	ID       string
	Tape     *tape.Tape
	Attached bool
}

type Manager struct {
	mu      sync.RWMutex
	current *Session
}

func NewManager() *Manager {
	return &Manager{}
}

func (m *Manager) Open(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current != nil && m.current.Attached {
		return nil, ErrActiveSessionExists
	}

	m.current = &Session{
		ID:       id,
		Tape:     &tape.Tape{SessionID: id},
		Attached: true,
	}
	return m.current, nil
}

func (m *Manager) Close(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == nil || m.current.ID != id {
		return ErrSessionNotFound
	}
	m.current = nil
	return nil
}
