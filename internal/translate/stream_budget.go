package translate

import (
	"encoding/json"
	"fmt"
)

// Shared by all items, including items waiting behind unfinished earlier output.
type streamBudget struct {
	bytes, blocks, queued int
	err                   error
}

func (b *streamBudget) reserve(n int) bool {
	if b == nil {
		return true
	}
	if b.err != nil {
		return false
	}
	if n > (32<<20)-b.bytes {
		b.err = fmt.Errorf("upstream stream retained content limit exceeded")
		return false
	}
	b.bytes += n
	return true
}

func (st *streamItem) queueEvent(e queuedEvent) {
	if st.budget != nil {
		if st.budget.queued >= 32768 {
			st.budget.err = fmt.Errorf("upstream stream queued event limit exceeded")
			return
		}
		if !st.budget.reserve(queuedBytes(e)) {
			return
		}
		st.budget.queued++
	}
	st.queue = append(st.queue, e)
}

func queuedBytes(e queuedEvent) int {
	n := len(e.delta)
	if e.citation != nil {
		raw, _ := json.Marshal(e.citation)
		n += len(raw)
	}
	return n
}

func (st *streamItem) reserveBlock() bool {
	if st.budget == nil {
		return true
	}
	if st.budget.blocks >= 32768 {
		st.budget.err = fmt.Errorf("upstream stream content block limit exceeded")
		return false
	}
	st.budget.blocks++
	return true
}
