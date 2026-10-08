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
