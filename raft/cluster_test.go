// Cluster tests: real nodes on the in-memory network from harness_test.go.

package raft

import (
	"math/rand/v2"
	"testing"
)

func TestInitialElection(t *testing.T) {
	c := newCluster(t, 3)
	c.checkOneLeader()

	term1 := c.checkTerms()
	if term1 < 1 {
		t.Fatalf("term is %d after an election, want >= 1", term1)
	}

	// with no failures the leader should stay put
	waitElectionTimeouts(4)
	if term2 := c.checkTerms(); term2 != term1 {
		t.Fatalf("term changed from %d to %d with no failures", term1, term2)
	}
	c.checkOneLeader()
}

func TestReElection(t *testing.T) {
	c := newCluster(t, 3)
	leader1 := c.checkOneLeader()

	// leader goes away, the other two elect a new one
	c.disconnect(leader1)
	c.checkOneLeader()

	// old leader comes back, there should still be exactly one leader
	c.connect(leader1)
	leader2 := c.checkOneLeader()

	// no quorum, no leader
	other := c.ids[0]
	if other == leader2 {
		other = c.ids[1]
	}
	c.disconnect(leader2)
	c.disconnect(other)
	waitElectionTimeouts(4)
	c.checkNoLeader()

	// quorum is back
	c.connect(other)
	c.checkOneLeader()

	c.connect(leader2)
	c.checkOneLeader()
}

func TestManyElections(t *testing.T) {
	c := newCluster(t, 7)
	c.checkOneLeader()

	for range 10 {
		// take 3 of 7 down, the other 4 are still a majority
		down := make([]NodeID, 0, 3)
		for _, i := range rand.Perm(len(c.ids))[:3] {
			down = append(down, c.ids[i])
			c.disconnect(c.ids[i])
		}
		c.checkOneLeader()

		for _, id := range down {
			c.connect(id)
		}
	}
	c.checkOneLeader()
}

func TestElectionUnreliableNetwork(t *testing.T) {
	c := newCluster(t, 5)
	c.net.setUnreliable(true)
	c.checkOneLeader()

	for range 5 {
		leader := c.checkOneLeader()
		c.disconnect(leader)
		c.checkOneLeader()
		c.connect(leader)
	}
}

func TestElectionAfterLeaderRestart(t *testing.T) {
	c := newCluster(t, 3)
	leader := c.checkOneLeader()
	term := c.checkTerms()

	c.restart(leader)
	c.checkOneLeader()
	if newTerm := c.checkTerms(); newTerm <= term {
		t.Fatalf("term went from %d to %d after the leader restarted, want it to grow", term, newTerm)
	}
}
