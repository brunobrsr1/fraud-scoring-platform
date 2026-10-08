// Unit tests for individual Raft rules. They are in package raft so they can
// set internal state directly.

package raft

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

var errFakeUnreachable = errors.New("fake transport: unreachable")

// fakeTransport returns scripted replies. A nil function simulates an
// unreachable peer.
type fakeTransport struct {
	mu      sync.Mutex
	vote    func(to NodeID, args *RequestVoteArgs) (*RequestVoteReply, error)
	append  func(to NodeID, args *AppendEntriesArgs) (*AppendEntriesReply, error)
	appends int // number of SendAppendEntries calls
}

func (f *fakeTransport) SendRequestVote(_ context.Context, to NodeID, args *RequestVoteArgs) (*RequestVoteReply, error) {
	f.mu.Lock()
	fn := f.vote
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeUnreachable
	}
	return fn(to, args)
}

func (f *fakeTransport) SendAppendEntries(_ context.Context, to NodeID, args *AppendEntriesArgs) (*AppendEntriesReply, error) {
	f.mu.Lock()
	f.appends++
	fn := f.append
	f.mu.Unlock()
	if fn == nil {
		return nil, errFakeUnreachable
	}
	return fn(to, args)
}

func (f *fakeTransport) appendCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.appends
}

// newTestNode builds an unstarted Node so tests can call handlers and set
// fields directly.
func newTestNode(t *testing.T, id NodeID, peers []NodeID, tr Transport) (*Node, *MemoryStorage, chan ApplyMsg) {
	t.Helper()
	if tr == nil {
		tr = &fakeTransport{}
	}
	storage := NewMemoryStorage()
	applyCh := make(chan ApplyMsg, 100)
	n, err := New(Config{ID: id, Peers: peers, Transport: tr, Storage: storage, ApplyCh: applyCh})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(n.Stop)
	return n, storage, applyCh
}

// threeNodes is the usual peer list for unit tests.
var threeNodes = []NodeID{"s1", "s2", "s3"}

// entries builds consecutive entries starting at index first, one per term.
func entries(first uint64, terms ...uint64) []Entry {
	out := make([]Entry, len(terms))
	for i, term := range terms {
		out[i] = Entry{Index: first + uint64(i), Term: term, Command: []byte{byte(first) + byte(i)}}
	}
	return out
}

// logWithTerms returns a fresh log holding entries 1..len(terms).
func logWithTerms(terms ...uint64) raftLog {
	l := newRaftLog()
	l.append(entries(1, terms...)...)
	return l
}

// logTerms returns the terms of all non-sentinel entries.
func logTerms(l *raftLog) []uint64 {
	var out []uint64
	for _, e := range l.entries[1:] {
		out = append(out, e.Term)
	}
	return out
}

// waitFor polls cond until it returns true or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}
