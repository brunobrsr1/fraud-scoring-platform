// Durable storage on the local disk

package raft

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const (
	stateFile = "state"
	tmpFile   = "state.tmp"
)

// FileStorage keeps the persistent state in a single file inside dir.
//
// Every Save rewrites the whole file: write state.tmp, fsync it, rename it
// over state, then fsync the directory so the rename itself is on disk. A
// crash at any point leaves either the old state or the new one, never half
// of each.
//
// Rewriting everything is O(log size) per Save and the node holds its lock
// during the fsync. That's fine for the registry (small log, rare writes) but
// it is the first thing to revisit if writes get frequent (OQ-1) or the log
// gets long (snapshots, OQ-2).
type FileStorage struct {
	mu   sync.Mutex
	dir  string
	size int
}

// NewFileStorage opens (or creates) the storage directory dir.
func NewFileStorage(dir string) (*FileStorage, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("raft: creating storage dir: %w", err)
	}
	f := &FileStorage{dir: dir}
	info, err := os.Stat(filepath.Join(dir, stateFile))
	switch {
	case err == nil:
		f.size = int(info.Size())
	case !errors.Is(err, os.ErrNotExist):
		return nil, fmt.Errorf("raft: opening storage: %w", err)
	}
	return f, nil
}

// Save implements Storage.
func (f *FileStorage) Save(state PersistentState) error {
	data, err := encodeGob(&state)
	if err != nil {
		return fmt.Errorf("raft: encoding state: %w", err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	tmp := filepath.Join(f.dir, tmpFile)
	if err := writeAndSync(tmp, data); err != nil {
		return fmt.Errorf("raft: writing state: %w", err)
	}
	if err := os.Rename(tmp, filepath.Join(f.dir, stateFile)); err != nil {
		return fmt.Errorf("raft: replacing state: %w", err)
	}
	if err := syncDir(f.dir); err != nil {
		return fmt.Errorf("raft: syncing storage dir: %w", err)
	}
	f.size = len(data)
	return nil
}

// Load implements Storage.
func (f *FileStorage) Load() (PersistentState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var state PersistentState
	data, err := os.ReadFile(filepath.Join(f.dir, stateFile))
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("raft: reading state: %w", err)
	}
	if err := decodeGob(data, &state); err != nil {
		return PersistentState{}, fmt.Errorf("raft: decoding state: %w", err)
	}
	return state, nil
}

// StateSize implements Storage.
func (f *FileStorage) StateSize() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.size
}

func writeAndSync(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
