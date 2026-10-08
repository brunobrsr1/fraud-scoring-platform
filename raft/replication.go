// Log replication: the leader side and the AppendEntries RPC

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

// replicateTo sends peer the entries from its nextIndex on (or a heartbeat
// if it is up to date) and handles the reply.
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
		Entries:      n.log.entriesFrom(prev+1, n.cfg.MaxEntriesPerAppend),
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
	if n.stopped || n.observeTerm(reply.Term) {
		return
	}
	if n.role != Leader || n.currentTerm != term {
		return
	}

	if reply.Success {
		// Replies can arrive out of order, so never move backwards.
		match := prev + uint64(len(args.Entries))
		n.matchIndex[peer] = max(n.matchIndex[peer], match)
		n.nextIndex[peer] = max(n.nextIndex[peer], match+1)
		n.advanceCommitIndex()
		return
	}

	// The follower doesn't have our entry at prev. Back up one entry and try
	// again right away. If nextIndex already moved, this reply is stale.
	if n.nextIndex[peer] == prev+1 && prev > 0 {
		n.nextIndex[peer] = prev
		n.spawn(func() { n.replicateTo(peer, term) })
	}
}

// advanceCommitIndex moves commitIndex up to the newest entry that a majority
// has stored. Only entries from the current term are counted: an older entry
// on a majority can still be overwritten by a future leader. Older entries
// get committed together with the first current-term entry after them.
// Must be called with mu held, on the leader.
func (n *Node) advanceCommitIndex() {
	for i := n.log.lastIndex(); i > n.commitIndex; i-- {
		if t, _ := n.log.term(i); t != n.currentTerm {
			break // terms only go down from here
		}
		count := 1 // ourselves
		for _, p := range n.peers {
			if n.matchIndex[p] >= i {
				count++
			}
		}
		if count >= n.quorum() {
			n.commitIndex = i
			n.applyCond.Broadcast()
			return
		}
	}
}

// HandleAppendEntries processes an AppendEntries RPC from the leader.
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

	// Skip entries we already have. Only truncate on a real conflict (same
	// index, different term): a delayed RPC carrying fewer entries must not
	// cut off entries that a newer RPC already gave us.
	for i, e := range args.Entries {
		if t, ok := n.log.term(e.Index); ok {
			if t == e.Term {
				continue
			}
			n.log.truncateFrom(e.Index)
		}
		n.log.append(args.Entries[i:]...)
		n.persist()
		break
	}

	// Only trust LeaderCommit up to what this RPC proved we share with the
	// leader. Anything after that in our log may still be wrong.
	lastNew := args.PrevLogIndex + uint64(len(args.Entries))
	if commit := min(args.LeaderCommit, lastNew); commit > n.commitIndex {
		n.commitIndex = commit
		n.applyCond.Broadcast()
	}

	reply.Success = true
	return reply, nil
}
