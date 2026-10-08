package raft

type Role int

const (
	Follower Role = iota // every node starts as a follower
	Candidate
	Leader
)

func (r Role) String() string {
	switch r {
	case Follower:
		return "Follower"
	case Candidate:
		return "Candidate"
	case Leader:
		return "Leader"
	default:
		return "Unknown"
	}
}

// observeTerm steps down to follower if term is newer than ours and reports
// whether it did. Every RPC request and reply goes through it.
func (n *Node) observeTerm(term uint64) bool {
	if term <= n.currentTerm {
		return false
	}
	n.becomeFollower(term)
	return true
}

func (n *Node) becomeFollower(term uint64) {
	wasLeader := n.role == Leader
	if term > n.currentTerm {
		n.currentTerm = term
		n.votedFor = ""
		n.leaderID = ""
		n.persist()
	}
	if n.role != Follower {
		n.logger.Debug("stepping down to follower", "term", n.currentTerm, "was", n.role)
	}
	n.role = Follower

	if wasLeader {
		n.resetElectionTimer()
	}
}

func (n *Node) becomeCandidate() {
	n.role = Candidate
	n.currentTerm++
	n.votedFor = n.id
	n.leaderID = ""
	n.persist()
	n.resetElectionTimer()
	n.logger.Debug("starting election", "term", n.currentTerm)
}

func (n *Node) becomeLeader() {
	n.role = Leader
	n.leaderID = n.id

	n.nextIndex = make(map[NodeID]uint64, len(n.peers))
	n.matchIndex = make(map[NodeID]uint64, len(n.peers))
	for _, p := range n.peers {
		n.nextIndex[p] = n.log.lastIndex() + 1
		n.matchIndex[p] = 0
	}

	// A leader can only count replicas for entries of its own term. The no-op
	// gives it one right away, so entries left over from older terms get
	// committed without waiting for the next real command.
	n.log.append(Entry{Index: n.log.lastIndex() + 1, Term: n.currentTerm})
	n.persist()
	n.advanceCommitIndex()
	n.logger.Debug("became leader", "term", n.currentTerm, "lastIndex", n.log.lastIndex())

	// A fresh channel per leadership, so a loop left over from an older term
	// can never swallow a kick meant for this one.
	n.kickCh = make(chan struct{}, 1)
	term, kickCh := n.currentTerm, n.kickCh
	n.spawn(func() { n.leaderLoop(term, kickCh) })
}
