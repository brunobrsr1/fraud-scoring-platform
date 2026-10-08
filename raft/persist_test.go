package raft

import "testing"

func TestRestartRestoresPersistentState(t *testing.T) {
	n, storage, _ := newTestNode(t, "s1", threeNodes, nil)
	n.mu.Lock()
	n.currentTerm, n.votedFor = 4, "s2"
	n.log.append(entries(1, 1, 3)...)
	n.persist()
	n.mu.Unlock()

	restarted, err := New(Config{ID: "s1", Peers: threeNodes, Transport: &fakeTransport{}, Storage: storage.Clone(), ApplyCh: make(chan ApplyMsg)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if restarted.currentTerm != 4 || restarted.votedFor != "s2" {
		t.Fatalf("term=%d votedFor=%q; want 4, s2", restarted.currentTerm, restarted.votedFor)
	}
	if got := logTerms(&restarted.log); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Fatalf("log terms = %v; want [1 3]", got)
	}
	if restarted.role != Follower {
		t.Fatalf("role = %v; a restarted node must come back as follower", restarted.role)
	}
}

func TestRestoreRejectsCorruptLog(t *testing.T) {
	storage := NewMemoryStorage()
	gap := append([]Entry{{}}, entries(1, 1)...)
	gap = append(gap, entries(3, 1)...) // index 2 is missing
	if err := storage.Save(PersistentState{CurrentTerm: 1, Entries: gap}); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{ID: "s1", Peers: threeNodes, Transport: &fakeTransport{}, Storage: storage, ApplyCh: make(chan ApplyMsg)}); err == nil {
		t.Fatal("New accepted a log with a gap in its indexes")
	}
}
