// In-memory Raft log with support for compaction

package raft

type EntryKind uint8 // distinguishes application commands from internal entries.

const (
	EntryCommand EntryKind = iota // carries an application command and is delivered on ApplyCh once committed
	EntryNoOp                     // appended by a new leader at the start of its term, never delivered to the application
)

// single log entry
type Entry struct {
	Index   uint64
	Term    uint64
	Kind    EntryKind
	Command []byte
}

type raftLog struct {
	entries []Entry
}

func newRaftLog() raftLog {
	return raftLog{entries: []Entry{{Index: 0, Term: 0}}}
}

func (l *raftLog) lastIndex() uint64 { return l.entries[len(l.entries)-1].Index }
func (l *raftLog) lastTerm() uint64  { return l.entries[len(l.entries)-1].Term }

// adds entries whose indexes directly follow lastIndex.
func (l *raftLog) append(entries ...Entry) {
	l.entries = append(l.entries, entries...)
}

// reports whether a log ending at (lastTerm, lastIndex) is at least as up-to-date as this log.
func (l *raftLog) isUpToDate(lastTerm, lastIndex uint64) bool {
	if lastTerm != l.lastTerm() {
		return lastTerm > l.lastTerm()
	}
	return lastIndex >= l.lastIndex()
}

// return the higest in-memory index holding term
func (l *raftLog) lastIndexOfTerm(term uint64) (index uint64, ok bool) {
	for p := len(l.entries) - 1; p >= 1; p-- {
		t := l.entries[p].Term
		if t == term {
			return l.entries[p].Index, true
		}
		if t < term {
			// Terms are non-decreasing along the log.
			break
		}
	}
	return 0, false
}

// discards the entire log, leaving only a sentinel (Figure 13 step 7).
func (l *raftLog) reset(index, term uint64) {
	l.entries = []Entry{{Index: index, Term: term}}
}
