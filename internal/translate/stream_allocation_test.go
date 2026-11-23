package translate

import (
	"runtime"
	"strings"
	"testing"
)

func TestStreamSmallChunksBoundedAllocations(t *testing.T) {
	s := streamState{items: map[int]*streamItem{}, emit: func(string, any) error { return nil }}
	if err := s.addItem(0, upstreamItem{Type: "function_call", CallID: "call", Name: "f"}); err != nil {
		t.Fatal(err)
	}
	st := s.items[0]
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for i := 0; i < 4096; i++ {
		if err := st.appendValue(st.blocks[0], "0123456789abcdef"); err != nil {
			t.Fatal(err)
		}
		if err := s.flush(); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	if st.blocks[0].value != strings.Repeat("0123456789abcdef", 4096) {
		t.Fatal("chunk concatenation changed")
	}
	if used := after.TotalAlloc - before.TotalAlloc; used > 16<<20 {
		t.Fatalf("64 KiB small-chunk stream allocated %d bytes, budget 16 MiB", used)
	}
}
