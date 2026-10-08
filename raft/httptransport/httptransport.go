// Package httptransport carries raft RPCs over HTTP with JSON bodies.
//
// Each node runs Handler on some address, and its peers reach it with a
// Client that maps node IDs to base URLs. Only the standard library is used,
// same as the scoring service.
package httptransport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"strings"

	"github.com/brunobrsr1/fraud-scoring-platform/raft"
)

const (
	RequestVotePath   = "/raft/v1/request-vote"
	AppendEntriesPath = "/raft/v1/append-entries"

	maxBodyBytes = 16 << 20 // a full AppendEntries batch fits easily
)

// Client sends raft RPCs to peers over HTTP. It implements raft.Transport.
type Client struct {
	peers map[raft.NodeID]string // node ID -> base URL, e.g. "http://raft-2:7000"
	http  *http.Client
}

var _ raft.Transport = (*Client)(nil)

// NewClient returns a Client for the given peers. No timeout is set on the
// http.Client, the raft node puts one on every request's context.
func NewClient(peers map[raft.NodeID]string) *Client {
	return &Client{peers: maps.Clone(peers), http: &http.Client{}}
}

// SendRequestVote implements raft.Transport.
func (c *Client) SendRequestVote(ctx context.Context, to raft.NodeID, args *raft.RequestVoteArgs) (*raft.RequestVoteReply, error) {
	var reply raft.RequestVoteReply
	if err := c.call(ctx, to, RequestVotePath, args, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

// SendAppendEntries implements raft.Transport.
func (c *Client) SendAppendEntries(ctx context.Context, to raft.NodeID, args *raft.AppendEntriesArgs) (*raft.AppendEntriesReply, error) {
	var reply raft.AppendEntriesReply
	if err := c.call(ctx, to, AppendEntriesPath, args, &reply); err != nil {
		return nil, err
	}
	return &reply, nil
}

func (c *Client) call(ctx context.Context, to raft.NodeID, path string, args, reply any) error {
	base, ok := c.peers[to]
	if !ok {
		return fmt.Errorf("httptransport: unknown node %q", to)
	}
	body, err := json.Marshal(args)
	if err != nil {
		return fmt.Errorf("httptransport: encoding request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("httptransport: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("httptransport: calling %s: %w", to, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("httptransport: %s returned %s: %s", to, resp.Status, strings.TrimSpace(string(msg)))
	}
	if err := json.NewDecoder(resp.Body).Decode(reply); err != nil {
		return fmt.Errorf("httptransport: decoding reply from %s: %w", to, err)
	}
	return nil
}

// Handler serves raft RPCs for node. A stopped node answers 503 so callers
// treat it like any other unreachable peer.
func Handler(node raft.RPCHandler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+RequestVotePath, func(w http.ResponseWriter, r *http.Request) {
		var args raft.RequestVoteArgs
		if !decode(w, r, &args) {
			return
		}
		reply, err := node.HandleRequestVote(&args)
		respond(w, reply, err)
	})
	mux.HandleFunc("POST "+AppendEntriesPath, func(w http.ResponseWriter, r *http.Request) {
		var args raft.AppendEntriesArgs
		if !decode(w, r, &args) {
			return
		}
		reply, err := node.HandleAppendEntries(&args)
		respond(w, reply, err)
	})
	return mux
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "bad request: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, reply any, err error) {
	switch {
	case errors.Is(err, raft.ErrStopped):
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(reply)
}
