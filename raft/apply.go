// Delivering committed entries to the service

package raft

// applier sends committed entries on applyCh in index order. It sends without
// holding mu, so a slow consumer can't block RPCs or elections.
func (n *Node) applier() {
	n.mu.Lock()
	defer n.mu.Unlock()
	for {
		for !n.stopped && n.lastApplied >= n.commitIndex {
			n.applyCond.Wait()
		}
		if n.stopped {
			return
		}

		batch := n.log.entriesFrom(n.lastApplied+1, int(n.commitIndex-n.lastApplied))
		n.mu.Unlock()

		for _, e := range batch {
			select {
			case n.applyCh <- ApplyMsg{Index: e.Index, Term: e.Term, Command: e.Command}:
			case <-n.stopCh:
				n.mu.Lock()
				return
			}
		}

		n.mu.Lock()
		n.lastApplied = batch[len(batch)-1].Index
	}
}
