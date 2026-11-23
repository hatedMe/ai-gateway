package translate

import (
	"fmt"
	"strings"
	"testing"
)

func TestResponsesStreamResourceLimits(t *testing.T) {
	for _, chat := range []bool{false, true} {
		t.Run(fmt.Sprint(chat), func(t *testing.T) {
			var source strings.Builder
			source.WriteString("data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_test\"}}\n\n")
			// Every individual frame is tiny; only the accumulated state is excessive.
			for i := 0; i <= 32768; i++ {
				fmt.Fprintf(&source, "data: {\"type\":\"response.output_item.added\",\"output_index\":%d,\"item\":{\"type\":\"message\"}}\n\n", i)
			}
			err := convertStreamFor(strings.NewReader(source.String()), "m", func(name string, _ any) error {
				if name == "message_stop" {
					t.Fatal("success after excessive state")
				}
				return nil
			}, chat)
			if err == nil || !strings.Contains(err.Error(), "limit") {
				t.Fatalf("want resource limit, got %v", err)
			}
		})
	}
}

func TestResponsesStreamByteAndQueueLimits(t *testing.T) {
	t.Run("content", func(t *testing.T) {
		s := streamState{items: map[int]*streamItem{}, emit: func(string, any) error { return nil }}
		if err := s.addItem(0, upstreamItem{Type: "function_call", CallID: "call", Name: "f"}); err != nil {
			t.Fatal(err)
		}
		if err := s.flush(); err != nil {
			t.Fatal(err)
		}
		st := s.items[0]
		for i := 0; i < 33; i++ {
			err := st.appendValue(st.blocks[0], strings.Repeat("x", 1<<20))
			if err != nil || s.budget.err != nil {
				if err := s.flush(); err == nil {
					t.Fatal("excessive retained state was emitted")
				}
				return
			}
			if err := s.flush(); err != nil {
				t.Fatal(err)
			}
		}
		t.Fatal("accepted 33 MiB")
	})
	t.Run("pending-and-release", func(t *testing.T) {
		s := streamState{items: map[int]*streamItem{}, emit: func(string, any) error { return nil }}
		_ = s.addItem(0, upstreamItem{Type: "function_call", CallID: "call", Name: "f"})
		st := s.items[0]
		for i := 0; i < 32767; i++ {
			if err := st.appendValue(st.blocks[0], "x"); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.flush(); err != nil {
			t.Fatal("exact queue limit:", err)
		}
		if s.budget.queued != 0 {
			t.Fatal("queue budget not released")
		}
		for i := 0; i < 32769; i++ {
			_ = st.appendValue(st.blocks[0], "x")
		}
		if err := s.flush(); err == nil {
			t.Fatal("queue limit bypassed")
		}
	})
	t.Run("reasoning", func(t *testing.T) {
		s := streamState{items: map[int]*streamItem{}}
		_ = s.addItem(0, upstreamItem{Type: "reasoning"})
		st := s.items[0]
		for i := 0; i < 33; i++ {
			if err := st.updateSummary(0, strings.Repeat("x", 1<<20), true, false); err != nil || s.budget.err != nil {
				return
			}
		}
		t.Fatal("summary bypassed budget")
	})
}
