// Heartbeats and the AppendEntries RPC

package raft

import (
	"context"
	"time"
)

// leaderLoop sends AppendEntries to every peer on each heartbeat tick or kick,
// for as long as this node is leader of term.
func (n *Node) leaderLoop(term uint64, kickCh <-chan struct{}) {
	t := time.NewTicker(n.cfg.HeartbeatInterval)
	defer t.Stop()
	for {
		n.mu.Lock()
		if n.stopped || n.role != Leader || n.currentTerm != term {
			n.mu.Unlock()
			return
		}
		for _, peer := range n.peers {
			n.spawn(func() { n.replicateTo(peer, term) })
		}
		n.mu.Unlock()

		select {
		case <-n.stopCh:
			return
		case <-t.C:
		case <-kickCh:
		}
	}
}

// replicateTo sends one AppendEntries RPC to peer and handles the reply.
// For now it only carries heartbeats.
func (n *Node) replicateTo(peer NodeID, term uint64) {
	n.mu.Lock()
	if n.role != Leader || n.currentTerm != term {
		n.mu.Unlock()
		return
	}
	prev := n.nextIndex[peer] - 1
	prevTerm, _ := n.log.term(prev)
	args := &AppendEntriesArgs{
		Term:         term,
		LeaderID:     n.id,
		PrevLogIndex: prev,
		PrevLogTerm:  prevTerm,
		LeaderCommit: n.commitIndex,
	}
	n.mu.Unlock()

	ctx, cancel := context.WithTimeout(n.ctx, n.cfg.RPCTimeout)
	reply, err := n.transport.SendAppendEntries(ctx, peer, args)
	cancel()
	if err != nil {
		// The next heartbeat retries.
		return
	}

	n.mu.Lock()
	defer n.mu.Unlock()
	if n.stopped {
		return
	}
	n.observeTerm(reply.Term)
}

// HandleAppendEntries processes an AppendEntries RPC. For now it handles the
// term rules and the log consistency check; entries are not stored yet.
func (n *Node) HandleAppendEntries(args *AppendEntriesArgs) (*AppendEntriesReply, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.stopped {
		return nil, ErrStopped
	}

	n.observeTerm(args.Term)

	reply := &AppendEntriesReply{Term: n.currentTerm}

	// A leader from an older term: tell it our term so it steps down.
	if args.Term < n.currentTerm {
		return reply, nil
	}

	// Same term: the sender is the one legitimate leader of this term, so a
	// candidate has lost the election.
	if n.role != Follower {
		n.becomeFollower(args.Term)
	}
	n.leaderID = args.LeaderID
	n.resetElectionTimer()

	// Our log must contain the entry just before the new ones, or the leader
	// has to back up and try an earlier point.
	if !n.log.matches(args.PrevLogIndex, args.PrevLogTerm) {
		return reply, nil
	}
	reply.Success = true
	return reply, nil
}
