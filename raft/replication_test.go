// Unit tests for the AppendEntries RPC. Helpers are in raft_test.go.

package raft

import (
	"testing"
	"time"
)

func TestAppendEntriesRejectsStaleTerm(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 3
	past := time.Now().Add(-time.Hour)
	n.electionDeadline = past

	reply, _ := n.HandleAppendEntries(&AppendEntriesArgs{Term: 2, LeaderID: "s2"})
	if reply.Success || reply.Term != 3 {
		t.Fatalf("reply = %+v; want rejected with Term=3", reply)
	}
	// An old leader must not keep us from starting an election.
	if !n.electionDeadline.Equal(past) || n.leaderID != "" {
		t.Fatal("a stale leader reset the election timer or became our leader")
	}
}

func TestAppendEntriesHigherTermStepsDown(t *testing.T) {
	n, storage, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm, n.role, n.votedFor = 2, Leader, "s1"

	reply, _ := n.HandleAppendEntries(&AppendEntriesArgs{Term: 5, LeaderID: "s3"})
	if !reply.Success || reply.Term != 5 {
		t.Fatalf("reply = %+v; want success with Term=5", reply)
	}
	if n.role != Follower || n.currentTerm != 5 || n.votedFor != "" || n.leaderID != "s3" {
		t.Fatalf("role=%v term=%d votedFor=%q leader=%q; want Follower, 5, \"\", s3",
			n.role, n.currentTerm, n.votedFor, n.leaderID)
	}
	// The new term must be on stable storage before the reply is sent.
	if st, _ := storage.Load(); st.CurrentTerm != 5 {
		t.Fatalf("persisted term = %d; want 5", st.CurrentTerm)
	}
}

func TestAppendEntriesSameTermCandidateStepsDown(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm, n.role, n.votedFor = 4, Candidate, "s1"

	_, _ = n.HandleAppendEntries(&AppendEntriesArgs{Term: 4, LeaderID: "s2"})
	if n.role != Follower || n.currentTerm != 4 || n.leaderID != "s2" {
		t.Fatalf("role=%v term=%d leader=%q; want Follower, 4, s2", n.role, n.currentTerm, n.leaderID)
	}
	// Same term: our vote for ourselves stands.
	if n.votedFor != "s1" {
		t.Fatalf("votedFor = %q; want s1 (same term, vote must not be cleared)", n.votedFor)
	}
}

func TestAppendEntriesResetsElectionTimer(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 1
	n.electionDeadline = time.Now().Add(-time.Hour)

	_, _ = n.HandleAppendEntries(&AppendEntriesArgs{Term: 1, LeaderID: "s2"})
	if !n.electionDeadline.After(time.Now()) {
		t.Fatal("a heartbeat from the current leader did not reset the election timer")
	}
}

func TestAppendEntriesConsistencyCheck(t *testing.T) {
	// Our log holds terms [1, 1, 2] at indexes 1..3.
	cases := []struct {
		prevIndex, prevTerm uint64
		success             bool
	}{
		{0, 0, true},  // empty prefix always matches
		{3, 2, true},  // matches our last entry
		{2, 1, true},  // matches a middle entry
		{3, 1, false}, // index exists, term differs
		{4, 2, false}, // index past our log
	}
	for _, c := range cases {
		n, _, _ := newTestNode(t, "s1", threeNodes, nil)
		n.currentTerm = 2
		n.log = logWithTerms(1, 1, 2)
		reply, _ := n.HandleAppendEntries(&AppendEntriesArgs{Term: 2, LeaderID: "s2", PrevLogIndex: c.prevIndex, PrevLogTerm: c.prevTerm})
		if reply.Success != c.success {
			t.Errorf("prev=(%d,%d): success=%v; want %v", c.prevIndex, c.prevTerm, reply.Success, c.success)
		}
	}
}

func TestLeaderStepsDownOnHigherTermHeartbeatReply(t *testing.T) {
	tr := &fakeTransport{
		vote: func(_ NodeID, args *RequestVoteArgs) (*RequestVoteReply, error) {
			return &RequestVoteReply{Term: args.Term, VoteGranted: true}, nil
		},
		append: func(_ NodeID, _ *AppendEntriesArgs) (*AppendEntriesReply, error) {
			return &AppendEntriesReply{Term: 9}, nil
		},
	}
	n, _, _ := newTestNode(t, "s1", threeNodes, tr)

	n.mu.Lock()
	n.startElection()
	n.mu.Unlock()

	waitFor(t, 2*time.Second, "leader to step down in term 9", func() bool {
		st := n.Status()
		return st.Role == Follower && st.Term == 9
	})
}

func TestAppendEntriesStoresNewEntries(t *testing.T) {
	n, storage, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 1

	reply, _ := n.HandleAppendEntries(&AppendEntriesArgs{Term: 1, LeaderID: "s2", Entries: entries(1, 1, 1)})
	if !reply.Success {
		t.Fatal("append to an empty log failed")
	}
	if got := logTerms(&n.log); len(got) != 2 {
		t.Fatalf("log terms = %v; want [1 1]", got)
	}
	if st, _ := storage.Load(); len(st.Entries) != 3 { // sentinel + 2
		t.Fatalf("persisted %d entries; want 3", len(st.Entries))
	}
}

func TestAppendEntriesTruncatesConflict(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 3
	n.log = logWithTerms(1, 1, 2, 2) // the term 2 entries never committed

	reply, _ := n.HandleAppendEntries(&AppendEntriesArgs{Term: 3, LeaderID: "s2", PrevLogIndex: 2, PrevLogTerm: 1, Entries: entries(3, 3)})
	if !reply.Success {
		t.Fatal("append failed")
	}
	got := logTerms(&n.log)
	if len(got) != 3 || got[2] != 3 {
		t.Fatalf("log terms = %v; want [1 1 3]", got)
	}
}

func TestAppendEntriesKeepsEntriesOnDelayedRPC(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 1
	n.log = logWithTerms(1, 1, 1)

	// an older RPC from the same leader shows up late with only the first entry
	_, _ = n.HandleAppendEntries(&AppendEntriesArgs{Term: 1, LeaderID: "s2", Entries: entries(1, 1)})
	if got := logTerms(&n.log); len(got) != 3 {
		t.Fatalf("log terms = %v; a delayed RPC must not truncate matching entries", got)
	}
}

func TestAppendEntriesCommitIndex(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 1
	n.log = logWithTerms(1, 1, 1, 1, 1)

	// leader has committed 5 but this RPC only proves we share up to 3
	_, _ = n.HandleAppendEntries(&AppendEntriesArgs{Term: 1, LeaderID: "s2", PrevLogIndex: 3, PrevLogTerm: 1, LeaderCommit: 5})
	if n.commitIndex != 3 {
		t.Fatalf("commitIndex = %d; want 3", n.commitIndex)
	}

	// an old heartbeat must not move it back
	_, _ = n.HandleAppendEntries(&AppendEntriesArgs{Term: 1, LeaderID: "s2", PrevLogIndex: 1, PrevLogTerm: 1, LeaderCommit: 5})
	if n.commitIndex != 3 {
		t.Fatalf("commitIndex = %d after an old heartbeat; want 3", n.commitIndex)
	}
}

func TestAdvanceCommitIndexOnlyCountsCurrentTerm(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm, n.role = 3, Leader
	n.log = logWithTerms(1, 2)
	n.matchIndex = map[NodeID]uint64{"s2": 2, "s3": 0}

	// index 2 is on a majority but it's from term 2
	n.advanceCommitIndex()
	if n.commitIndex != 0 {
		t.Fatalf("commitIndex = %d; an old-term entry must not be committed by counting", n.commitIndex)
	}

	// once a term 3 entry is on a majority, everything before it commits too
	n.log.append(entries(3, 3)...)
	n.matchIndex["s2"] = 3
	n.advanceCommitIndex()
	if n.commitIndex != 3 {
		t.Fatalf("commitIndex = %d; want 3", n.commitIndex)
	}
}

func TestProposeErrors(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	if _, _, err := n.Propose([]byte("x")); err != ErrNotLeader {
		t.Fatalf("Propose on follower: err = %v; want ErrNotLeader", err)
	}

	n.mu.Lock()
	n.role = Leader
	n.mu.Unlock()
	if _, _, err := n.Propose(nil); err != ErrEmptyCommand {
		t.Fatalf("Propose(nil): err = %v; want ErrEmptyCommand", err)
	}
}

func TestSingleNodeCommitsAndApplies(t *testing.T) {
	n, _, applyCh := newTestNode(t, "solo", []NodeID{"solo"}, nil)
	n.Start()
	waitFor(t, 2*time.Second, "single node to become leader", func() bool { return n.Status().Role == Leader })

	index, _, err := n.Propose([]byte("hello"))
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	// first the leader's no-op, then our command
	for want := uint64(1); want <= index; want++ {
		select {
		case m := <-applyCh:
			if m.Index != want {
				t.Fatalf("applied index %d; want %d", m.Index, want)
			}
			if m.Index == index && string(m.Command) != "hello" {
				t.Fatalf("applied %q; want hello", m.Command)
			}
		case <-time.After(time.Second):
			t.Fatalf("index %d never applied", want)
		}
	}
}
