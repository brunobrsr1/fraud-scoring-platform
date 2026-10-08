// In-memory network and cluster helpers for the cluster tests.
//
// Set RAFT_DEBUG=1 to see node logs.

package raft

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"os"
	"sync"
	"testing"
	"time"
)

var errUnreachable = errors.New("test network: unreachable")

// network routes RPCs between nodes in the same process. A node can be
// disconnected (all its RPCs fail, both ways) and the whole network can be
// made unreliable (random delays and dropped requests/replies).
type network struct {
	mu         sync.Mutex
	handlers   map[NodeID]RPCHandler
	connected  map[NodeID]bool
	unreliable bool
}

func newNetwork() *network {
	return &network{
		handlers:  map[NodeID]RPCHandler{},
		connected: map[NodeID]bool{},
	}
}

// endpoint is the Transport one node uses to talk to the others.
type endpoint struct {
	net  *network
	from NodeID
}

func (e *endpoint) SendRequestVote(ctx context.Context, to NodeID, args *RequestVoteArgs) (*RequestVoteReply, error) {
	h, err := e.net.request(ctx, e.from, to)
	if err != nil {
		return nil, err
	}
	reply, err := h.HandleRequestVote(args)
	if err != nil {
		return nil, err
	}
	if err := e.net.reply(ctx, e.from, to); err != nil {
		return nil, err
	}
	return reply, nil
}

func (e *endpoint) SendAppendEntries(ctx context.Context, to NodeID, args *AppendEntriesArgs) (*AppendEntriesReply, error) {
	h, err := e.net.request(ctx, e.from, to)
	if err != nil {
		return nil, err
	}
	reply, err := h.HandleAppendEntries(args)
	if err != nil {
		return nil, err
	}
	if err := e.net.reply(ctx, e.from, to); err != nil {
		return nil, err
	}
	return reply, nil
}

// request decides if a request from -> to gets delivered, and returns the
// handler to call if it does.
func (nw *network) request(ctx context.Context, from, to NodeID) (RPCHandler, error) {
	if err := nw.maybeDelayOrDrop(ctx); err != nil {
		return nil, err
	}
	nw.mu.Lock()
	defer nw.mu.Unlock()
	h := nw.handlers[to]
	if h == nil || !nw.connected[from] || !nw.connected[to] {
		return nil, errUnreachable
	}
	return h, nil
}

// reply decides if the reply makes it back. Either side may have been
// disconnected while the handler ran.
func (nw *network) reply(ctx context.Context, from, to NodeID) error {
	if err := nw.maybeDelayOrDrop(ctx); err != nil {
		return err
	}
	nw.mu.Lock()
	defer nw.mu.Unlock()
	if !nw.connected[from] || !nw.connected[to] {
		return errUnreachable
	}
	return nil
}

func (nw *network) maybeDelayOrDrop(ctx context.Context) error {
	nw.mu.Lock()
	unreliable := nw.unreliable
	nw.mu.Unlock()
	if !unreliable {
		return ctx.Err()
	}

	select {
	case <-time.After(time.Duration(rand.IntN(27)) * time.Millisecond):
	case <-ctx.Done():
		return ctx.Err()
	}
	if rand.IntN(10) == 0 { // drop 10%
		return errUnreachable
	}
	return nil
}

func (nw *network) setHandler(id NodeID, h RPCHandler) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	if h == nil {
		delete(nw.handlers, id)
		return
	}
	nw.handlers[id] = h
}

func (nw *network) setConnected(id NodeID, ok bool) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	nw.connected[id] = ok
}

func (nw *network) isConnected(id NodeID) bool {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	return nw.connected[id]
}

func (nw *network) setUnreliable(on bool) {
	nw.mu.Lock()
	defer nw.mu.Unlock()
	nw.unreliable = on
}

// cluster is a set of nodes on one test network.
type cluster struct {
	t        *testing.T
	net      *network
	ids      []NodeID
	logger   *slog.Logger
	mu       sync.Mutex
	nodes    map[NodeID]*Node // nil while a node is crashed
	storages map[NodeID]*MemoryStorage
}

func newCluster(t *testing.T, size int) *cluster {
	t.Helper()
	c := &cluster{
		t:        t,
		net:      newNetwork(),
		logger:   slog.New(slog.DiscardHandler),
		nodes:    map[NodeID]*Node{},
		storages: map[NodeID]*MemoryStorage{},
	}
	if os.Getenv("RAFT_DEBUG") != "" {
		c.logger = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	for i := range size {
		c.ids = append(c.ids, NodeID(string(rune('a'+i))))
	}
	for _, id := range c.ids {
		c.storages[id] = NewMemoryStorage()
		c.start(id)
		c.connect(id)
	}
	t.Cleanup(c.shutdown)
	return c
}

// start boots a node from whatever is in its storage.
func (c *cluster) start(id NodeID) {
	c.t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()

	n, err := New(Config{
		ID:        id,
		Peers:     c.ids,
		Transport: &endpoint{net: c.net, from: id},
		Storage:   c.storages[id],
		ApplyCh:   make(chan ApplyMsg, 1000),
		Logger:    c.logger,
	})
	if err != nil {
		c.t.Fatalf("starting %s: %v", id, err)
	}
	c.nodes[id] = n
	c.net.setHandler(id, n)
	n.Start()
}

// crash stops a node. Its storage is cloned so the dead node can't write into
// what the next incarnation reads.
func (c *cluster) crash(id NodeID) {
	c.mu.Lock()
	n := c.nodes[id]
	c.nodes[id] = nil
	c.mu.Unlock()
	if n == nil {
		return
	}
	c.net.setHandler(id, nil)
	n.Stop()

	c.mu.Lock()
	c.storages[id] = c.storages[id].Clone()
	c.mu.Unlock()
}

func (c *cluster) restart(id NodeID) {
	c.crash(id)
	c.start(id)
}

func (c *cluster) connect(id NodeID)    { c.net.setConnected(id, true) }
func (c *cluster) disconnect(id NodeID) { c.net.setConnected(id, false) }

func (c *cluster) shutdown() {
	for _, id := range c.ids {
		c.crash(id)
	}
}

func (c *cluster) node(id NodeID) *Node {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.nodes[id]
}

// liveStatuses returns the status of every node that is running and connected.
func (c *cluster) liveStatuses() []Status {
	var out []Status
	for _, id := range c.ids {
		n := c.node(id)
		if n == nil || !c.net.isConnected(id) {
			continue
		}
		out = append(out, n.Status())
	}
	return out
}

// checkOneLeader waits until exactly one connected node thinks it is leader
// and returns it. Two leaders in the same term fails the test straight away.
//
// An old leader that was just reconnected can still think it leads an older
// term until it hears from the new one, so we wait for that to settle instead
// of picking the newest term.
func (c *cluster) checkOneLeader() NodeID {
	c.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		leaders := map[uint64]NodeID{}
		for _, st := range c.liveStatuses() {
			if st.Role != Leader {
				continue
			}
			if other, ok := leaders[st.Term]; ok {
				c.t.Fatalf("term %d has two leaders: %s and %s", st.Term, other, st.ID)
			}
			leaders[st.Term] = st.ID
		}
		if len(leaders) == 1 {
			for _, id := range leaders {
				return id
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatal("expected exactly one leader")
	return ""
}

// checkNoLeader fails if any connected node thinks it is leader.
func (c *cluster) checkNoLeader() {
	c.t.Helper()
	for _, st := range c.liveStatuses() {
		if st.Role == Leader {
			c.t.Fatalf("%s is leader in term %d, expected no leader", st.ID, st.Term)
		}
	}
}

// checkTerms waits for every connected node to agree on the term and returns it.
func (c *cluster) checkTerms() uint64 {
	c.t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var term uint64
		agree := true
		for _, st := range c.liveStatuses() {
			if term == 0 {
				term = st.Term
			} else if st.Term != term {
				agree = false
				break
			}
		}
		if agree {
			return term
		}
		time.Sleep(20 * time.Millisecond)
	}
	c.t.Fatal("nodes never agreed on a term")
	return 0
}

// waits long enough for any election that is going to happen to happen
func waitElectionTimeouts(k int) {
	time.Sleep(time.Duration(k) * DefaultElectionTimeoutMax)
}
