package translate

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkRetainedStreamText(b *testing.B) {
	for _, size := range []int{64 << 10, 1 << 20} {
		for _, chunkSize := range []int{16, 256, 4096} {
			b.Run(fmt.Sprintf("bytes=%d/chunk=%d", size, chunkSize), func(b *testing.B) {
				chunk := strings.Repeat("x", chunkSize)
				b.ReportAllocs()
				b.SetBytes(int64(size))
				for n := 0; n < b.N; n++ {
					s := streamState{items: map[int]*streamItem{}, emit: func(string, any) error { return nil }}
					if err := s.addItem(0, upstreamItem{Type: "function_call", CallID: "call", Name: "f"}); err != nil {
						b.Fatal(err)
					}
					st := s.items[0]
					for i := 0; i < size/chunkSize; i++ {
						if err := st.appendValue(st.blocks[0], chunk); err != nil {
							b.Fatal(err)
						}
						if err := s.flush(); err != nil {
							b.Fatal(err)
						}
					}
				}
			})
		}
	}
}

func BenchmarkRetainedReasoningSummary(b *testing.B) {
	chunk := strings.Repeat("x", 256)
	b.ReportAllocs()
	for n := 0; n < b.N; n++ {
		s := streamState{items: map[int]*streamItem{}, emit: func(string, any) error { return nil }}
		if err := s.addItem(0, upstreamItem{Type: "reasoning"}); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < 256; i++ {
			if err := s.items[0].updateSummary(0, chunk, true, false); err != nil {
				b.Fatal(err)
			}
			if err := s.flush(); err != nil {
				b.Fatal(err)
			}
		}
	}
}
