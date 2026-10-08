// Stable storage
//
// currentTerm, votedFor and the log must survive crashes: losing the term or
// vote could allow two leaders in one term, and losing acknowledged entries
// could lose committed data.

package raft

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"sync"
)

// PersistentState is the state that must survive a crash (Figure 2).
type PersistentState struct {
	CurrentTerm uint64
	VotedFor    NodeID
	Entries     []Entry // Entries[0] is the log sentinel (see raftLog)
}

// Storage persists a Node's state. It is called with the Node's lock held.
// Implementations must be safe for concurrent use and must not retain
// state.Entries after Save returns.
type Storage interface {
	// Save durably stores state. A returned error is treated as fatal.
	Save(state PersistentState) error

	// Load returns the last saved state, or the zero value on fresh storage.
	Load() (PersistentState, error)

	// StateSize returns the size in bytes of the stored state.
	StateSize() int
}

func encodeGob(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func decodeGob(data []byte, v any) error {
	return gob.NewDecoder(bytes.NewReader(data)).Decode(v)
}

// MemoryStorage is an in-memory Storage for tests. It stores gob-encoded
// bytes so encoding errors and sizes are realistic and saved data can't be
// mutated through shared slices.
type MemoryStorage struct {
	mu    sync.Mutex
	state []byte
}

// NewMemoryStorage returns an empty MemoryStorage.
func NewMemoryStorage() *MemoryStorage {
	return &MemoryStorage{}
}

// Save implements Storage.
func (m *MemoryStorage) Save(state PersistentState) error {
	st, err := encodeGob(&state)
	if err != nil {
		return fmt.Errorf("raft: encoding state: %w", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = st
	return nil
}

// Load implements Storage.
func (m *MemoryStorage) Load() (PersistentState, error) {
	m.mu.Lock()
	st := m.state
	m.mu.Unlock()

	var state PersistentState
	if st != nil {
		if err := decodeGob(st, &state); err != nil {
			return PersistentState{}, fmt.Errorf("raft: decoding state: %w", err)
		}
	}
	return state, nil
}

// StateSize implements Storage.
func (m *MemoryStorage) StateSize() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.state)
}

// Clone returns an independent copy. Tests use it to simulate a crash, so a
// stopped node can never write into its restarted successor's storage.
func (m *MemoryStorage) Clone() *MemoryStorage {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &MemoryStorage{state: bytes.Clone(m.state)}
}
