package raft

import (
	"os"
	"path/filepath"
	"testing"
)

func testState() PersistentState {
	return PersistentState{
		CurrentTerm: 7,
		VotedFor:    "s2",
		Entries:     append([]Entry{{}}, entries(1, 1, 3, 7)...),
	}
}

func sameState(t *testing.T, got, want PersistentState) {
	t.Helper()
	if got.CurrentTerm != want.CurrentTerm || got.VotedFor != want.VotedFor || len(got.Entries) != len(want.Entries) {
		t.Fatalf("got term=%d votedFor=%q %d entries; want term=%d votedFor=%q %d entries",
			got.CurrentTerm, got.VotedFor, len(got.Entries), want.CurrentTerm, want.VotedFor, len(want.Entries))
	}
	for i := range want.Entries {
		g, w := got.Entries[i], want.Entries[i]
		if g.Index != w.Index || g.Term != w.Term || string(g.Command) != string(w.Command) {
			t.Fatalf("entry %d = %+v; want %+v", i, g, w)
		}
	}
}

func TestFileStorageEmptyDir(t *testing.T) {
	fs, err := NewFileStorage(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	st, err := fs.Load()
	if err != nil {
		t.Fatalf("Load on fresh storage: %v", err)
	}
	if st.CurrentTerm != 0 || st.VotedFor != "" || len(st.Entries) != 0 {
		t.Fatalf("fresh storage returned %+v; want the zero value", st)
	}
	if fs.StateSize() != 0 {
		t.Fatalf("StateSize = %d; want 0", fs.StateSize())
	}
}

func TestFileStorageSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	fs, err := NewFileStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := testState()
	if err := fs.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	size := fs.StateSize()

	// a new process opening the same directory
	reopened, err := NewFileStorage(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sameState(t, got, want)
	if reopened.StateSize() != size || size == 0 {
		t.Fatalf("StateSize after reopen = %d; want %d (non-zero)", reopened.StateSize(), size)
	}
}

func TestFileStorageLastSaveWins(t *testing.T) {
	fs, err := NewFileStorage(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_ = fs.Save(PersistentState{CurrentTerm: 1})
	want := testState()
	if err := fs.Save(want); err != nil {
		t.Fatal(err)
	}
	got, _ := fs.Load()
	sameState(t, got, want)
}

// a crash between writing state.tmp and the rename leaves the tmp file
// behind, that must not affect what we load
func TestFileStorageIgnoresLeftoverTmp(t *testing.T) {
	dir := t.TempDir()
	fs, _ := NewFileStorage(dir)
	want := testState()
	if err := fs.Save(want); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, tmpFile), []byte("half written garbage"), 0o644); err != nil {
		t.Fatal(err)
	}

	reopened, _ := NewFileStorage(dir)
	got, err := reopened.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	sameState(t, got, want)

	// and the next Save still works
	if err := reopened.Save(PersistentState{CurrentTerm: 8}); err != nil {
		t.Fatalf("Save over a leftover tmp file: %v", err)
	}
}

func TestFileStorageCorruptStateIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, stateFile), []byte("not gob"), 0o644); err != nil {
		t.Fatal(err)
	}
	fs, _ := NewFileStorage(dir)
	if _, err := fs.Load(); err == nil {
		t.Fatal("Load returned no error for a corrupt state file")
	}
}

func TestNodeRestartsFromFileStorage(t *testing.T) {
	dir := t.TempDir()
	fs, _ := NewFileStorage(dir)
	n, err := New(Config{ID: "s1", Peers: threeNodes, Transport: &fakeTransport{}, Storage: fs, ApplyCh: make(chan ApplyMsg)})
	if err != nil {
		t.Fatal(err)
	}
	n.mu.Lock()
	n.currentTerm, n.votedFor = 3, "s3"
	n.log.append(entries(1, 2, 3)...)
	n.persist()
	n.mu.Unlock()

	fs2, _ := NewFileStorage(dir)
	restarted, err := New(Config{ID: "s1", Peers: threeNodes, Transport: &fakeTransport{}, Storage: fs2, ApplyCh: make(chan ApplyMsg)})
	if err != nil {
		t.Fatal(err)
	}
	if restarted.currentTerm != 3 || restarted.votedFor != "s3" || restarted.log.lastIndex() != 2 {
		t.Fatalf("term=%d votedFor=%q lastIndex=%d; want 3, s3, 2",
			restarted.currentTerm, restarted.votedFor, restarted.log.lastIndex())
	}
}
