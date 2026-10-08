// In-memory Raft log with support for compaction

package raft

import "fmt"

// single log entry
type Entry struct {
	Index   uint64
	Term    uint64
	Command []byte
}

type raftLog struct {
	entries []Entry
}

func newRaftLog() raftLog {
	return raftLog{entries: []Entry{{Index: 0, Term: 0}}}
}
func (l *raftLog) snapshotIndex() uint64 { return l.entries[0].Index }
func (l *raftLog) snapshotTerm() uint64  { return l.entries[0].Term }
func (l *raftLog) lastIndex() uint64     { return l.entries[len(l.entries)-1].Index }
func (l *raftLog) lastTerm() uint64      { return l.entries[len(l.entries)-1].Term }

// maps a log index to a slice position. Requires snapshotIndex <= i <= lastIndex
func (l *raftLog) pos(i uint64) int { return int(i - l.snapshotIndex()) }

// returns the term at the index i
func (l *raftLog) term(i uint64) (term uint64, ok bool) {
	if i < l.snapshotIndex() || i > l.lastIndex() {
		return 0, false
	}
	return l.entries[l.pos(i)].Term, true
}

// returns a copy of up to limit entries starting at lo
func (l *raftLog) entriesFrom(lo uint64, limit int) []Entry {
	if lo <= l.snapshotIndex() {
		panic(fmt.Sprintf("raft: entriesFrom(%d) but entries up to %d were compacted", lo, l.snapshotIndex()))
	}
	if lo > l.lastIndex() {
		return nil
	}
	src := l.entries[l.pos(lo):]
	if limit > 0 && len(src) > limit {
		src = src[:limit]
	}
	out := make([]Entry, len(src))
	copy(out, src)
	return out
}

// adds entries whose indexes directly follow lastIndex
func (l *raftLog) append(entries ...Entry) {
	l.entries = append(l.entries, entries...)
}

// deletes the entry at index i and all that follow it
func (l *raftLog) truncateFrom(i uint64) {
	if i <= l.snapshotIndex() {
		panic(fmt.Sprintf("raft: truncateFrom(%d) would delete compacted (committed) entries up to %d", i, l.snapshotIndex()))
	}
	if i > l.lastIndex() {
		return
	}
	l.entries = l.entries[:l.pos(i)]
}

// matches reports whether the log has an entry at index with the given term
func (l *raftLog) matches(index, term uint64) bool {
	t, ok := l.term(index)
	return ok && t == term
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

// discards entries up to and including index, keeping the suffix
func (l *raftLog) compactTo(index, term uint64) {
	suffix := l.entries[l.pos(index)+1:]
	// Allocate a new slice so the old backing array can be garbage
	// collected.
	fresh := make([]Entry, 0, 1+len(suffix))
	fresh = append(fresh, Entry{Index: index, Term: term})
	fresh = append(fresh, suffix...)
	l.entries = fresh
}

// discards the entire log, leaving only a sentinel (Figure 13 step 7).
func (l *raftLog) reset(index, term uint64) {
	l.entries = []Entry{{Index: index, Term: term}}
}
