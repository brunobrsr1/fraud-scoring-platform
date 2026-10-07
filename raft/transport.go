// This file defines how Raft talks to the network, as two small interfaces.
// The two directions:
//
//	  outgoing                                incoming
//	Node ──► Transport.SendXxx ──network──► RPCHandler.HandleXxx ──► Node
//	         (implemented by a                (implemented by *Node;
//	          transport package)               a transport calls it)
package raft

import "context"

type Transport interface {
	// SendRequestVote asks node `to` for its vote (§5.2).
	SendRequestVote(ctx context.Context, to NodeID, args *RequestVoteArgs) (*RequestVoteReply, error)

	// SendAppendEntries sends log entries or a heartbeat to node `to`
	SendAppendEntries(ctx context.Context, to NodeID, args *AppendEntriesArgs) (*AppendEntriesReply, error)
}

type RPCHandler interface {
	// HandleRequestVote processes a RequestVote RPC (Figure 2).
	HandleRequestVote(args *RequestVoteArgs) (*RequestVoteReply, error)

	// HandleAppendEntries processes an AppendEntries RPC (Figure 2).
	HandleAppendEntries(args *AppendEntriesArgs) (*AppendEntriesReply, error)
}

var _ RPCHandler = (*Node)(nil) // Check that Node implements RPCHandler
