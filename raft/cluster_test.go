// Cluster tests: real nodes on the in-memory network from harness_test.go.

package raft

import (
	"fmt"
	"math/rand/v2"
	"sync"
	"testing"
	"time"
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

func TestBasicAgree(t *testing.T) {
	c := newCluster(t, 3)
	c.checkOneLeader()

	var last uint64
	for i := range 5 {
		index := c.one(cmd(i), 3)
		if index <= last {
			t.Fatalf("cmd %d got index %d, previous was %d", i, index, last)
		}
		last = index
	}
}

func TestFollowerFailure(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(101), 3)

	leader := c.checkOneLeader()
	c.disconnect(otherThan(c, leader))

	// two out of three is still a majority
	c.one(cmd(102), 2)
	waitElectionTimeouts(1)
	c.one(cmd(103), 2)

	// the last follower goes too, the leader can't commit anything now
	leader = c.checkOneLeader()
	c.disconnect(connectedFollower(c, leader))
	index, _, err := c.node(leader).Propose([]byte(cmd(104)))
	if err != nil {
		t.Fatalf("Propose on leader: %v", err)
	}
	waitElectionTimeouts(2)
	if count, _ := c.nCommitted(index); count > 0 {
		t.Fatalf("%d nodes committed index %d without a majority", count, index)
	}
}

func TestLeaderFailure(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(101), 3)

	leader1 := c.checkOneLeader()
	c.disconnect(leader1)
	c.one(cmd(102), 2)
	waitElectionTimeouts(1)
	c.one(cmd(103), 2)

	// second leader gone too, one node left, nothing can commit
	leader2 := c.checkOneLeader()
	c.disconnect(leader2)
	index, _, err := c.node(leader2).Propose([]byte(cmd(104)))
	if err != nil {
		t.Fatalf("Propose on old leader: %v", err)
	}
	waitElectionTimeouts(2)
	if count, got := c.nCommitted(index); count > 0 && string(got) == cmd(104) {
		t.Fatalf("%d nodes committed %s without a majority", count, cmd(104))
	}
}

// a follower that missed some entries catches up after it reconnects
func TestFailAgree(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(101), 3)

	leader := c.checkOneLeader()
	follower := otherThan(c, leader)
	c.disconnect(follower)
	for i := 102; i <= 105; i++ {
		c.one(cmd(i), 2)
	}

	c.connect(follower)
	c.one(cmd(106), 3)
	c.one(cmd(107), 3)
}

func TestFailNoAgree(t *testing.T) {
	c := newCluster(t, 5)
	c.one(cmd(10), 5)

	// 3 of 5 followers gone
	leader := c.checkOneLeader()
	down := 0
	for _, id := range c.ids {
		if id != leader && down < 3 {
			c.disconnect(id)
			down++
		}
	}

	index, _, err := c.node(leader).Propose([]byte(cmd(20)))
	if err != nil {
		t.Fatalf("Propose on leader: %v", err)
	}
	waitElectionTimeouts(2)
	if count, _ := c.nCommitted(index); count > 0 {
		t.Fatalf("%d nodes committed index %d without a majority", count, index)
	}

	for _, id := range c.ids {
		c.connect(id)
	}
	c.one(cmd(30), 5)
}

// an old leader's uncommitted entries get thrown away when it rejoins
func TestRejoin(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(101), 3)

	leader1 := c.checkOneLeader()
	c.disconnect(leader1)
	for i := 102; i <= 104; i++ {
		_, _, _ = c.node(leader1).Propose([]byte(cmd(i))) // never commit
	}

	c.one(cmd(103), 2)

	leader2 := c.checkOneLeader()
	c.disconnect(leader2)
	c.connect(leader1)
	c.one(cmd(104), 2)

	c.connect(leader2)
	c.one(cmd(105), 3)
}

// leader has to back up over a lot of conflicting entries on its followers
func TestBackup(t *testing.T) {
	c := newCluster(t, 5)
	c.one(cmd(0), 5)

	// leader and one follower keep going on their own, 50 entries that
	// will never commit
	leader1 := c.checkOneLeader()
	friend := otherThan(c, leader1)
	var rest []NodeID
	for _, id := range c.ids {
		if id != leader1 && id != friend {
			rest = append(rest, id)
			c.disconnect(id)
		}
	}
	for i := range 50 {
		_, _, _ = c.node(leader1).Propose([]byte(cmd(1000 + i)))
	}
	waitElectionTimeouts(1)

	// the other three take over and commit 50 of their own
	c.disconnect(leader1)
	c.disconnect(friend)
	for _, id := range rest {
		c.connect(id)
	}
	for i := range 50 {
		c.one(cmd(2000+i), 3)
	}

	// their new leader loses one follower and piles up 50 uncommitted entries
	leader2 := c.checkOneLeader()
	out := rest[0]
	if out == leader2 {
		out = rest[1]
	}
	c.disconnect(out)
	for i := range 50 {
		_, _, _ = c.node(leader2).Propose([]byte(cmd(3000 + i)))
	}
	waitElectionTimeouts(1)

	// old leader + friend come back with the follower that has the
	// committed entries, it has to win and fix both their logs
	for _, id := range c.ids {
		c.disconnect(id)
	}
	c.connect(leader1)
	c.connect(friend)
	c.connect(out)
	for i := range 50 {
		c.one(cmd(4000+i), 3)
	}

	for _, id := range c.ids {
		c.connect(id)
	}
	c.one(cmd(5000), 5)
}

func TestConcurrentProposals(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(0), 3)
	leader := c.checkOneLeader()

	var wg sync.WaitGroup
	indexes := make([]uint64, 5)
	for i := range 5 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			index, _, err := c.node(leader).Propose([]byte(cmd(100 + i)))
			if err != nil {
				t.Errorf("Propose: %v", err)
			}
			indexes[i] = index
		}()
	}
	wg.Wait()

	for i, index := range indexes {
		waitFor(t, 2*time.Second, fmt.Sprintf("cmd %d committed on all nodes", i), func() bool {
			count, got := c.nCommitted(index)
			return count == 3 && string(got) == cmd(100+i)
		})
	}
}

// connectedFollower returns a connected node that isn't the leader
func connectedFollower(c *cluster, leader NodeID) NodeID {
	for _, id := range c.ids {
		if id != leader && c.net.isConnected(id) {
			return id
		}
	}
	return ""
}

// otherThan returns some node that isn't id
func otherThan(c *cluster, id NodeID) NodeID {
	for _, other := range c.ids {
		if other != id {
			return other
		}
	}
	return ""
}
