package raft

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type NodeID string // identifies a node in the cluster. Empty string means "nobody"

// Errors returned by Node methods
var (
	ErrNotLeader    = errors.New("raft: not the leader")
	ErrStopped      = errors.New("raft: node is stopped")
	ErrEmptyCommand = errors.New("raft: empty command")
)

// broadcastTime << electionTimeout << MTBF
const (
	DefaultElectionTimeoutMin  = 150 * time.Millisecond
	DefaultElectionTimeoutMax  = 300 * time.Millisecond
	DefaultHeartbeatInterval   = 50 * time.Millisecond
	DefaultRPCTimeout          = 1 * time.Second
	DefaultMaxEntriesPerAppend = 1000
)

// ApplyMsg delivers one committed log entry to the service, in index order.
// An empty Command is a no-op entry written by a new leader; the service should
// skip it. There are no snapshots, so after a restart every entry is delivered
// again from index 1 and the service rebuilds its state from scratch.
type ApplyMsg struct {
	Index   uint64
	Term    uint64
	Command []byte
}

type Config struct {
	ID        NodeID          // unique ID of this node
	Peers     []NodeID        // IDs of all nodes in the cluster (including this one)
	Transport Transport       // transport layer for sending and receiving RPCs
	Storage   Storage         // storage layer for persisting state and log entries
	ApplyCh   chan<- ApplyMsg // channel for sending committed log entries to the service

	ElectionTimeoutMin  time.Duration // minimum election timeout
	ElectionTimeoutMax  time.Duration // maximum election timeout
	HeartbeatInterval   time.Duration // heartbeat interval
	RPCTimeout          time.Duration // RPC timeout
	MaxEntriesPerAppend int           // maximum number of log entries to send in one AppendEntries RPC
	Logger              *slog.Logger  // receives debug output about elections, term changes
}

// returns a copy of c with default values for every unset field
func (c Config) withDefaults() Config {
	if c.ElectionTimeoutMin == 0 {
		c.ElectionTimeoutMin = DefaultElectionTimeoutMin
	}
	if c.ElectionTimeoutMax == 0 {
		c.ElectionTimeoutMax = DefaultElectionTimeoutMax
	}
	if c.HeartbeatInterval == 0 {
		c.HeartbeatInterval = DefaultHeartbeatInterval
	}
	if c.RPCTimeout == 0 {
		c.RPCTimeout = DefaultRPCTimeout
	}
	if c.MaxEntriesPerAppend == 0 {
		c.MaxEntriesPerAppend = DefaultMaxEntriesPerAppend
	}
	if c.Logger == nil {
		c.Logger = slog.New(slog.DiscardHandler)
	}
	return c
}

func (c Config) validate() error {
	if c.ID == "" {
		return errors.New("raft: Config.ID is empty")
	}
	seen := make(map[NodeID]bool, len(c.Peers))
	for _, peer := range c.Peers {
		if peer == "" {
			return errors.New("raft: Config.Peers contains an empty ID")
		}
		if seen[peer] {
			return errors.New("raft: Config.Peers contains duplicate IDs")
		}
		seen[peer] = true
	}
	if !seen[c.ID] {
		return fmt.Errorf("raft: Config.Peers must include this node's own ID %q", c.ID)
	}
	if c.Transport == nil {
		return errors.New("raft: Config.Transport is nil")
	}
	if c.Storage == nil {
		return errors.New("raft: Config.Storage is nil")
	}
	if c.ApplyCh == nil {
		return errors.New("raft: Config.ApplyCh is nil")
	}
	if c.ElectionTimeoutMin <= 0 || c.ElectionTimeoutMax <= c.ElectionTimeoutMin {
		return fmt.Errorf("raft: need 0 < ElectionTimeoutMin < ElectionTimeoutMax, got %v and %v",
			c.ElectionTimeoutMin, c.ElectionTimeoutMax)
	}
	if c.HeartbeatInterval <= 0 || c.HeartbeatInterval >= c.ElectionTimeoutMin {
		return fmt.Errorf("raft: need 0 < HeartbeatInterval < ElectionTimeoutMin, got %v and %v",
			c.HeartbeatInterval, c.ElectionTimeoutMin)
	}
	if c.RPCTimeout <= 0 {
		return errors.New("raft: Config.RPCTimeout must be positive")
	}
	if c.MaxEntriesPerAppend < 0 {
		return errors.New("raft: Config.MaxEntriesPerAppend must not be negative")
	}
	return nil
}

// point-in-time view of a Node
type Status struct {
	ID           NodeID
	Role         Role
	Term         uint64
	LeaderID     NodeID // "" if unknown
	CommitIndex  uint64
	LastApplied  uint64
	LastLogIndex uint64
}

type Node struct {
	mu sync.Mutex

	// Fixed at construction. Never modified, safe to read without mu
	id        NodeID
	peers     []NodeID // every member EXCEPT this node
	cfg       Config
	transport Transport
	storage   Storage
	applyCh   chan<- ApplyMsg
	logger    *slog.Logger

	// Persistent state on all nodes. Saved by persist()
	// before replying to any RPC that depended on a change
	currentTerm uint64
	votedFor    NodeID
	log         raftLog

	// Volatile state on all nodes
	commitIndex uint64 // highest log index known to be committed
	lastApplied uint64 // highest log index delivered on applyCh

	// Volatile state on leaders. Reinitialized after every election
	nextIndex  map[NodeID]uint64 // for each server, index of the next log entry to send to that server
	matchIndex map[NodeID]uint64 // for each server, index of highest log entry known to be replicated on server

	// Bookkeeping state
	role             Role
	leaderID         NodeID        // "" if unknown
	electionDeadline time.Time     // when this node will start a new election
	applyCond        *sync.Cond    // signaled when commitIndex or lastApplied changes
	kickCh           chan struct{} // wakes the current leader loop; replaced on every election

	// Lifecycle
	started bool
	stopped bool
	ctx     context.Context
	cancel  context.CancelFunc
	stopCh  chan struct{}
	wg      sync.WaitGroup
}

func New(cfg Config) (*Node, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	state, err := cfg.Storage.Load()
	if err != nil {
		return nil, fmt.Errorf("raft: loading persistent state: %w", err)
	}

	n := &Node{
		id:        cfg.ID,
		cfg:       cfg,
		transport: cfg.Transport,
		storage:   cfg.Storage,
		applyCh:   cfg.ApplyCh,
		logger:    cfg.Logger.With("node", string(cfg.ID)),
		role:      Follower,
		stopCh:    make(chan struct{}),
	}
	for _, p := range cfg.Peers {
		if p != cfg.ID {
			n.peers = append(n.peers, p)
		}
	}
	n.applyCond = sync.NewCond(&n.mu)
	n.ctx, n.cancel = context.WithCancel(context.Background())

	if err := n.restoreState(state); err != nil {
		return nil, err
	}

	n.resetElectionTimer()
	n.logger.Debug("node created", "term", n.currentTerm,
		"lastIndex", n.log.lastIndex(), "lastTerm", n.log.lastTerm())
	return n, nil
}

func (n *Node) Start() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.started || n.stopped {
		return
	}
	n.started = true
	// Give the cluster a full timeout to show us a leader before we try to
	// become one ourselves.
	n.resetElectionTimer()
	n.spawn(n.ticker)
	n.spawn(n.applier)
}

func (n *Node) Stop() {
	n.mu.Lock()
	if n.stopped {
		n.mu.Unlock()
		return
	}
	n.stopped = true
	close(n.stopCh)         // wake goroutines waiting on channels
	n.cancel()              // abort RPCs in flight
	n.applyCond.Broadcast() // wake the applier if it is waiting for work
	n.mu.Unlock()

	// Wait WITHOUT holding the lock: the goroutines we are waiting for may
	// need the lock to notice they should exit.
	n.wg.Wait()
	n.logger.Debug("node stopped")
}

// Propose appends cmd to the leader's log and starts replicating it. It
// returns right away; the entry is only committed once it shows up on
// ApplyCh. If leadership changes first, the entry may never commit and the
// caller should retry with the new leader.
func (n *Node) Propose(cmd []byte) (index, term uint64, err error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.stopped {
		return 0, 0, ErrStopped
	}
	if n.role != Leader {
		return 0, 0, ErrNotLeader
	}
	// Empty commands are how no-ops look, and gob stores nil and empty the
	// same way, so they can't be told apart after a restart.
	if len(cmd) == 0 {
		return 0, 0, ErrEmptyCommand
	}

	index = n.log.lastIndex() + 1
	n.log.append(Entry{Index: index, Term: n.currentTerm, Command: bytes.Clone(cmd)})
	// The leader counts itself toward the majority, so the entry has to be on
	// our own disk first.
	n.persist()
	n.advanceCommitIndex() // a single-node cluster commits right away
	n.kick()
	return index, n.currentTerm, nil
}

func (n *Node) Status() Status {
	n.mu.Lock()
	defer n.mu.Unlock()
	return Status{
		ID:           n.id,
		Role:         n.role,
		Term:         n.currentTerm,
		LeaderID:     n.leaderID,
		CommitIndex:  n.commitIndex,
		LastApplied:  n.lastApplied,
		LastLogIndex: n.log.lastIndex(),
	}
}

func (n *Node) quorum() int {
	clusterSize := len(n.peers) + 1
	return clusterSize/2 + 1
}

// kick wakes the leader loop so it replicates now instead of at the next
// heartbeat. Never blocks, one pending kick is enough.
func (n *Node) kick() {
	select {
	case n.kickCh <- struct{}{}:
	default:
	}
}

// spawn runs f in a goroutine that Stop waits for. Must be called with mu held.
func (n *Node) spawn(f func()) {
	if n.stopped {
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		f()
	}()
}
