// Unit tests for leader election and the voting restriction. Helpers are in
// raft_test.go.

package raft

import (
	"testing"
	"time"
)

func TestRequestVoteRejectsStaleTerm(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 3

	reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 2, CandidateID: "s2"})
	if reply.VoteGranted || reply.Term != 3 {
		t.Fatalf("reply = %+v; want denied with Term=3", reply)
	}
}

func TestRequestVoteOneVotePerTerm(t *testing.T) {
	n, storage, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 1

	reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 1, CandidateID: "s2"})
	if !reply.VoteGranted {
		t.Fatal("first vote of the term should be granted")
	}
	// The vote must be on stable storage before the reply is sent.
	if st, _ := storage.Load(); st.VotedFor != "s2" {
		t.Fatalf("persisted votedFor = %q; want s2", st.VotedFor)
	}

	if reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 1, CandidateID: "s3"}); reply.VoteGranted {
		t.Fatal("second candidate in the same term must be denied")
	}
	// The same candidate asking again (a retransmission) gets the same answer.
	if reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 1, CandidateID: "s2"}); !reply.VoteGranted {
		t.Fatal("repeated request from the candidate we voted for should be granted")
	}
}

func TestRequestVoteNewTermClearsVote(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm, n.votedFor = 1, "s2"

	reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 2, CandidateID: "s3"})
	if !reply.VoteGranted || n.currentTerm != 2 || n.votedFor != "s3" {
		t.Fatalf("granted=%v term=%d votedFor=%q; want true, 2, s3", reply.VoteGranted, n.currentTerm, n.votedFor)
	}
}

func TestRequestVoteElectionRestriction(t *testing.T) {
	// Our log ends with (index 2, term 2).
	cases := []struct {
		lastTerm, lastIndex uint64
		grant               bool
	}{
		{1, 5, false}, // older last term: missing our term-2 entry
		{2, 1, false}, // same last term but shorter
		{2, 2, true},  // identical
		{3, 1, true},  // newer last term
	}
	for _, c := range cases {
		n, _, _ := newTestNode(t, "s1", threeNodes, nil)
		n.currentTerm = 3
		n.log = logWithTerms(1, 2)
		reply, _ := n.HandleRequestVote(&RequestVoteArgs{Term: 3, CandidateID: "s2", LastLogTerm: c.lastTerm, LastLogIndex: c.lastIndex})
		if reply.VoteGranted != c.grant {
			t.Errorf("candidate last=(%d,%d): granted=%v; want %v", c.lastIndex, c.lastTerm, reply.VoteGranted, c.grant)
		}
	}
}

func TestOnlyGrantedVotesResetTheTimer(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.currentTerm = 2
	n.log = logWithTerms(1, 2)
	past := time.Now().Add(-time.Hour)

	// A denied vote must not delay our own election.
	n.electionDeadline = past
	_, _ = n.HandleRequestVote(&RequestVoteArgs{Term: 2, CandidateID: "s2", LastLogTerm: 1, LastLogIndex: 1})
	if !n.electionDeadline.Equal(past) {
		t.Fatal("denying a vote reset the election timer")
	}

	// A granted vote does reset it.
	_, _ = n.HandleRequestVote(&RequestVoteArgs{Term: 2, CandidateID: "s3", LastLogTerm: 2, LastLogIndex: 2})
	if !n.electionDeadline.After(time.Now()) {
		t.Fatal("granting a vote did not reset the election timer")
	}
}

func TestElectionTimeoutIsRandomizedWithinBounds(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	lo, hi := n.cfg.ElectionTimeoutMin, n.cfg.ElectionTimeoutMax

	seen := map[time.Duration]bool{}
	for range 200 {
		before := time.Now()
		n.resetElectionTimer()
		after := time.Now()
		if n.electionDeadline.Before(before.Add(lo)) || n.electionDeadline.After(after.Add(hi)) {
			t.Fatalf("deadline %v outside [%v, %v] from now", n.electionDeadline.Sub(before), lo, hi)
		}
		seen[n.electionDeadline.Sub(before).Round(time.Millisecond)] = true
	}
	if len(seen) < 10 {
		t.Fatalf("only %d distinct timeouts in 200 draws; timeouts must be randomized", len(seen))
	}
}

func TestCandidateWinsWithMajority(t *testing.T) {
	peers := []NodeID{"s1", "s2", "s3", "s4", "s5"}
	tr := &fakeTransport{
		// s2 and s3 vote yes, s4 and s5 say no. With our own vote: 3 of 5.
		vote: func(to NodeID, args *RequestVoteArgs) (*RequestVoteReply, error) {
			return &RequestVoteReply{Term: args.Term, VoteGranted: to == "s2" || to == "s3"}, nil
		},
		append: func(_ NodeID, args *AppendEntriesArgs) (*AppendEntriesReply, error) {
			return &AppendEntriesReply{Term: args.Term, Success: true}, nil
		},
	}
	n, _, _ := newTestNode(t, "s1", peers, tr)

	n.mu.Lock()
	n.startElection()
	if n.role != Candidate || n.currentTerm != 1 || n.votedFor != "s1" {
		t.Fatalf("after startElection: role=%v term=%d votedFor=%q; want Candidate, 1, s1", n.role, n.currentTerm, n.votedFor)
	}
	n.mu.Unlock()

	waitFor(t, 2*time.Second, "s1 to become leader", func() bool { return n.Status().Role == Leader })

	// The leader must start sending heartbeats immediately.
	waitFor(t, time.Second, "heartbeats", func() bool { return tr.appendCount() >= len(peers)-1 })
}

func TestCandidateWithoutMajorityStaysCandidate(t *testing.T) {
	tr := &fakeTransport{
		vote: func(_ NodeID, args *RequestVoteArgs) (*RequestVoteReply, error) {
			return &RequestVoteReply{Term: args.Term, VoteGranted: false}, nil
		},
	}
	n, _, _ := newTestNode(t, "s1", threeNodes, tr)

	n.mu.Lock()
	n.startElection()
	n.mu.Unlock()

	time.Sleep(50 * time.Millisecond)
	if st := n.Status(); st.Role != Candidate {
		t.Fatalf("role = %v; want Candidate (no majority)", st.Role)
	}
}

func TestCandidateStepsDownOnHigherTerm(t *testing.T) {
	tr := &fakeTransport{
		vote: func(_ NodeID, _ *RequestVoteArgs) (*RequestVoteReply, error) {
			return &RequestVoteReply{Term: 7, VoteGranted: false}, nil
		},
	}
	n, _, _ := newTestNode(t, "s1", threeNodes, tr)

	n.mu.Lock()
	n.startElection()
	n.mu.Unlock()

	waitFor(t, time.Second, "step down to follower in term 7", func() bool {
		st := n.Status()
		return st.Role == Follower && st.Term == 7
	})
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.votedFor != "" {
		t.Fatalf("votedFor = %q after moving to a new term; want empty", n.votedFor)
	}
}

func TestLeaderStepsDownOnHigherTermVoteRequest(t *testing.T) {
	n, _, _ := newTestNode(t, "s1", threeNodes, nil)
	n.mu.Lock()
	n.currentTerm, n.role = 2, Leader
	n.mu.Unlock()

	_, _ = n.HandleRequestVote(&RequestVoteArgs{Term: 3, CandidateID: "s2"})
	if st := n.Status(); st.Role != Follower || st.Term != 3 {
		t.Fatalf("role=%v term=%d; want Follower in term 3", st.Role, st.Term)
	}
}

func TestSingleNodeClusterElectsItself(t *testing.T) {
	n, _, _ := newTestNode(t, "solo", []NodeID{"solo"}, nil)
	n.Start()

	waitFor(t, 2*time.Second, "single node to become leader", func() bool { return n.Status().Role == Leader })
}
