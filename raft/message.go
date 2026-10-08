package raft

// RequestVoteArgs is sent by a candidate to every other node to ask for its vote
type RequestVoteArgs struct {
	Term         uint64 // candidate’s term
	CandidateID  NodeID // candidate requesting vote
	LastLogIndex uint64 // index of candidate’s last log entry
	LastLogTerm  uint64 // term of candidate’s last log entry
}

// RequestVoteReply is the response to a RequestVoteArgs
type RequestVoteReply struct {
	Term        uint64 // voter's term, for candidate to update itself if it is behind
	VoteGranted bool   // true means candidate received vote
}

// AppendEntriesArgs is sent by a leader to replicate log entries
type AppendEntriesArgs struct {
	Term         uint64  // leader’s term
	LeaderID     NodeID  // so follower can redirect clients
	PrevLogIndex uint64  // index of log entry immediately preceding new ones
	PrevLogTerm  uint64  // term of prevLogIndex entry
	Entries      []Entry // log entries to store (empty for heartbeat; may send more than one for efficiency)
	LeaderCommit uint64  // leader’s commitIndex. The follower uses it to learn what entries are safe to apply
}

// AppendEntriesReply is the response to an AppendEntriesArgs
type AppendEntriesReply struct {
	Term    uint64 // currentTerm, for leader to update itself
	Success bool   // true if follower contained entry matching prevLogIndex and prevLogTerm
}
