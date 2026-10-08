// Leader election and the voting restriction

package raft

import (
	"context"
	"math/rand/v2"
	"time"
)

const tickInterval = 10 * time.Millisecond

// starts an election whenever a non-leader's deadline passes
func (n *Node) ticker() {
	t := time.NewTicker(tickInterval)
	defer t.Stop()
	for {
		select {
		case <-n.stopCh:
			return
		case <-t.C:
		}

		n.mu.Lock()
		if !n.stopped && n.role != Leader && time.Now().After(n.electionDeadline) {
			n.startElection()
		}
		n.mu.Unlock()
	}
}

// sets a new randomized deadline
func (n *Node) resetElectionTimer() {
	spread := n.cfg.ElectionTimeoutMax - n.cfg.ElectionTimeoutMin
	timeout := n.cfg.ElectionTimeoutMin + time.Duration(rand.Int64N(int64(spread)))
	n.electionDeadline = time.Now().Add(timeout)
}

// becomes a candidate and requests votes from all peers
func (n *Node) startElection() {
	n.becomeCandidate()

	// Counts our own vote
	votes := 1
	if votes >= n.quorum() {
		n.becomeLeader()
		return
	}

	args := &RequestVoteArgs{
		Term:         n.currentTerm,
		CandidateID:  n.id,
		LastLogIndex: n.log.lastIndex(),
		LastLogTerm:  n.log.lastTerm(),
	}

	for _, peer := range n.peers {
		n.spawn(func() { n.requestVote(peer, args, &votes) })
	}
}

// sends one RequestVote RPC and tallies the result
func (n *Node) requestVote(peer NodeID, args *RequestVoteArgs, votes *int) {
	ctx, cancel := context.WithTimeout(n.ctx, n.cfg.RPCTimeout)
	reply, err := n.transport.SendRequestVote(ctx, peer, args)
	cancel()
	if err != nil {
		// No retry needed, a new election starts if this one times out
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()

	if n.stopped || n.observeTerm(reply.Term) {
		return
	}
	// Ignores replies from an election that has already ended
	if n.role != Candidate || args.Term != n.currentTerm {
		return
	}
	if !reply.VoteGranted {
		return
	}

	*votes++
	if *votes >= n.quorum() {
		n.becomeLeader()
	}
}

// handles the RequestVote RPC
func (n *Node) HandleRequestVote(args *RequestVoteArgs) (*RequestVoteReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.stopped {
		return nil, ErrStopped
	}

	n.observeTerm(args.Term)

	reply := &RequestVoteReply{Term: n.currentTerm}

	if args.Term < n.currentTerm {
		return reply, nil
	}

	// One vote per term, and only to a candidate whose log is at least as up-to-date as ours.
	canVote := n.votedFor == "" || n.votedFor == args.CandidateID
	if canVote && n.log.isUpToDate(args.LastLogTerm, args.LastLogIndex) {
		n.votedFor = args.CandidateID
		// Persist before replying so a restart can't lead to a double vote.
		n.persist()
		n.resetElectionTimer()
		reply.VoteGranted = true
	}
	return reply, nil
}
