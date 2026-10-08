package httptransport_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brunobrsr1/fraud-scoring-platform/raft"
	"github.com/brunobrsr1/fraud-scoring-platform/raft/httptransport"
)

// fakeHandler answers with fixed replies or a fixed error.
type fakeHandler struct {
	err error
}

func (f *fakeHandler) HandleRequestVote(args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &raft.RequestVoteReply{Term: args.Term, VoteGranted: args.CandidateID == "b"}, nil
}

func (f *fakeHandler) HandleAppendEntries(args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &raft.AppendEntriesReply{Term: args.Term, Success: len(args.Entries) == 2}, nil
}

func TestRoundTrip(t *testing.T) {
	srv := httptest.NewServer(httptransport.Handler(&fakeHandler{}))
	defer srv.Close()
	c := httptransport.NewClient(map[raft.NodeID]string{"a": srv.URL})

	vote, err := c.SendRequestVote(context.Background(), "a", &raft.RequestVoteArgs{Term: 4, CandidateID: "b"})
	if err != nil {
		t.Fatalf("SendRequestVote: %v", err)
	}
	if vote.Term != 4 || !vote.VoteGranted {
		t.Fatalf("vote reply = %+v; want term 4, granted", vote)
	}

	app, err := c.SendAppendEntries(context.Background(), "a", &raft.AppendEntriesArgs{
		Term:    4,
		Entries: []raft.Entry{{Index: 1, Term: 4, Command: []byte{0, 1, 2}}, {Index: 2, Term: 4}},
	})
	if err != nil {
		t.Fatalf("SendAppendEntries: %v", err)
	}
	if app.Term != 4 || !app.Success {
		t.Fatalf("append reply = %+v; want term 4, success (both entries arrived)", app)
	}
}

func TestStoppedNodeIs503(t *testing.T) {
	srv := httptest.NewServer(httptransport.Handler(&fakeHandler{err: raft.ErrStopped}))
	defer srv.Close()
	c := httptransport.NewClient(map[raft.NodeID]string{"a": srv.URL})

	_, err := c.SendRequestVote(context.Background(), "a", &raft.RequestVoteArgs{Term: 1})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("err = %v; want a 503 error", err)
	}
}

func TestBadRequestBody(t *testing.T) {
	srv := httptest.NewServer(httptransport.Handler(&fakeHandler{}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+httptransport.AppendEntriesPath, "application/json", strings.NewReader("{not json"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400", resp.StatusCode)
	}
}

func TestUnknownPeer(t *testing.T) {
	c := httptransport.NewClient(nil)
	if _, err := c.SendRequestVote(context.Background(), "nope", &raft.RequestVoteArgs{}); err == nil {
		t.Fatal("expected an error for a node that isn't in the peer map")
	}
}

func TestContextCancelStopsCall(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-block:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(block)
	c := httptransport.NewClient(map[raft.NodeID]string{"a": srv.URL})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := c.SendRequestVote(ctx, "a", &raft.RequestVoteArgs{}); err == nil {
		t.Fatal("expected a timeout error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("call kept going long after the context expired")
	}
}

// --- end to end: three real nodes talking HTTP over loopback ---

type testNode struct {
	id   raft.NodeID
	node *raft.Node
	srv  *httptest.Server

	mu      sync.Mutex
	applied []string // non no-op commands, in order
}

func (n *testNode) commands() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.applied...)
}

func startCluster(t *testing.T) []*testNode {
	t.Helper()
	ids := []raft.NodeID{"a", "b", "c"}

	// Listeners first so every node knows every URL before anything starts.
	nodes := make([]*testNode, len(ids))
	peers := map[raft.NodeID]string{}
	for i, id := range ids {
		srv := httptest.NewUnstartedServer(nil)
		nodes[i] = &testNode{id: id, srv: srv}
		peers[id] = "http://" + srv.Listener.Addr().String()
	}

	for _, tn := range nodes {
		storage, err := raft.NewFileStorage(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		applyCh := make(chan raft.ApplyMsg, 64)
		node, err := raft.New(raft.Config{
			ID:        tn.id,
			Peers:     ids,
			Transport: httptransport.NewClient(peers),
			Storage:   storage,
			ApplyCh:   applyCh,
		})
		if err != nil {
			t.Fatal(err)
		}
		tn.node = node
		tn.srv.Config.Handler = httptransport.Handler(node)
		tn.srv.Start()

		done := make(chan struct{})
		go func() {
			for {
				select {
				case m := <-applyCh:
					if len(m.Command) > 0 {
						tn.mu.Lock()
						tn.applied = append(tn.applied, string(m.Command))
						tn.mu.Unlock()
					}
				case <-done:
					return
				}
			}
		}()
		t.Cleanup(func() {
			tn.node.Stop()
			tn.srv.Close()
			close(done)
		})
	}
	for _, tn := range nodes {
		tn.node.Start()
	}
	return nodes
}

func waitLeader(t *testing.T, nodes []*testNode) *testNode {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, tn := range nodes {
			if tn.node.Status().Role == raft.Leader {
				return tn
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("no leader elected over HTTP")
	return nil
}

func waitApplied(t *testing.T, nodes []*testNode, want ...string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		ok := true
		for _, tn := range nodes {
			if strings.Join(tn.commands(), ",") != strings.Join(want, ",") {
				ok = false
			}
		}
		if ok {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	for _, tn := range nodes {
		t.Logf("%s applied %v", tn.id, tn.commands())
	}
	t.Fatalf("nodes did not all apply %v", want)
}

func TestClusterOverHTTP(t *testing.T) {
	nodes := startCluster(t)

	leader := waitLeader(t, nodes)
	if _, _, err := leader.node.Propose([]byte("promote v1.0.1")); err != nil {
		t.Fatalf("Propose: %v", err)
	}
	waitApplied(t, nodes, "promote v1.0.1")

	// kill the leader, the other two take over and keep committing
	leader.node.Stop()
	leader.srv.Close()
	var rest []*testNode
	for _, tn := range nodes {
		if tn != leader {
			rest = append(rest, tn)
		}
	}

	leader2 := waitLeader(t, rest)
	if _, _, err := leader2.node.Propose([]byte("promote v1.0.2")); err != nil {
		t.Fatalf("Propose on new leader: %v", err)
	}
	waitApplied(t, rest, "promote v1.0.1", "promote v1.0.2")
}
