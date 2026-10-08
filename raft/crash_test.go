// Crash/restart tests. A crashed node comes back with only what it persisted,
// so if persist is missing anywhere these lose committed entries or let a
// node vote twice in a term.

package raft

import (
	"math/rand/v2"
	"testing"
	"time"
)

func TestPersist1(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(11), 3)

	// everyone crashes at once
	for _, id := range c.ids {
		c.crash(id)
	}
	for _, id := range c.ids {
		c.start(id)
	}
	c.one(cmd(12), 3)

	leader1 := c.checkOneLeader()
	c.restart(leader1)
	c.one(cmd(13), 3)

	leader2 := c.checkOneLeader()
	c.crash(leader2)
	c.one(cmd(14), 2)
	c.start(leader2)
	c.one(cmd(15), 3)
}

func TestPersist2(t *testing.T) {
	c := newCluster(t, 5)

	for iter := range 5 {
		c.one(cmd(10+iter), 5)

		leader := c.checkOneLeader()
		var f []NodeID
		for _, id := range c.ids {
			if id != leader {
				f = append(f, id)
			}
		}

		c.crash(f[0])
		c.crash(f[1])
		c.one(cmd(20+iter), 3)

		// everyone down, then back up with only f[2] holding the last entry.
		// with 3 of 5 up every vote counts, so f[2] is the only one that can win
		c.crash(leader)
		c.crash(f[2])
		c.crash(f[3])
		c.start(f[0])
		c.start(f[1])
		c.start(f[2])
		c.one(cmd(30+iter), 3)

		c.start(leader)
		c.start(f[3])
	}
	c.one(cmd(1000), 5)
}

func TestPersist3(t *testing.T) {
	c := newCluster(t, 3)
	c.one(cmd(101), 3)

	leader := c.checkOneLeader()
	var f []NodeID
	for _, id := range c.ids {
		if id != leader {
			f = append(f, id)
		}
	}

	c.disconnect(f[1])
	c.one(cmd(102), 2)

	c.crash(leader)
	c.crash(f[0])
	c.connect(f[1])
	c.start(leader)
	c.one(cmd(103), 2)

	c.start(f[0])
	c.one(cmd(104), 3)
}

// Leaders keep getting crashed while they have entries in flight. Lots of
// half-replicated entries from different terms end up in the logs, and the
// apply checker makes sure nobody ever commits the wrong one.
func TestChaosReliable(t *testing.T) {
	c := newCluster(t, 5)
	c.one(cmd(0), 1)

	next := 1
	up := len(c.ids)
	for range 200 {
		if leader, ok := c.anyLeader(); ok {
			_, _, _ = c.node(leader).Propose([]byte(cmd(next)))
			next++
			sleepChaos()
			if rand.IntN(2) == 0 {
				c.crash(leader)
				up--
			}
		} else {
			sleepChaos()
		}

		// keep a majority alive so the cluster can make progress
		if up < 3 {
			if id, ok := c.anyCrashed(); ok {
				c.start(id)
				up++
			}
		}
	}

	for {
		id, ok := c.anyCrashed()
		if !ok {
			break
		}
		c.start(id)
	}
	c.one(cmd(next), 5)
}

// Same idea on an unreliable network, with disconnects as well as crashes.
func TestChaosUnreliable(t *testing.T) {
	c := newCluster(t, 5)
	c.net.setUnreliable(true)
	c.one(cmd(0), 1)

	next := 1
	for range 200 {
		if leader, ok := c.anyLeader(); ok {
			_, _, _ = c.node(leader).Propose([]byte(cmd(next)))
			next++
			if rand.IntN(4) == 0 {
				c.disconnect(leader)
			} else if rand.IntN(10) == 0 {
				c.crash(leader)
				c.start(leader)
			}
		}
		sleepChaos()

		down := 0
		for _, id := range c.ids {
			if !c.net.isConnected(id) {
				down++
			}
		}
		if down > 2 {
			for _, id := range c.ids {
				if !c.net.isConnected(id) {
					c.connect(id)
					break
				}
			}
		}
	}

	c.net.setUnreliable(false)
	for _, id := range c.ids {
		c.connect(id)
	}
	c.one(cmd(next), 5)
}

// mostly short pauses, sometimes long enough for an election
func sleepChaos() {
	if rand.IntN(10) == 0 {
		time.Sleep(time.Duration(rand.Int64N(int64(DefaultElectionTimeoutMax))))
	} else {
		time.Sleep(time.Duration(rand.IntN(13)) * time.Millisecond)
	}
}
