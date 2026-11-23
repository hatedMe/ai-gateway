package translate

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// readSSE joins data fields per frame; neither network reads nor lines are events.
func readSSE(r io.Reader, consume func([]byte) (bool, error)) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	var data strings.Builder
	dispatch := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		raw := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		return consume([]byte(raw))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			stop, err := dispatch()
			if err != nil {
				return err
			}
			if stop {
				return nil
			}
			continue
		}
		field, value, found := strings.Cut(line, ":")
		if !found {
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		if field == "data" {
			if data.Len()+len(value) > 4<<20 {
				return fmt.Errorf("upstream SSE frame exceeds 4 MiB")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	stop, err := dispatch()
	if err != nil {
		return err
	}
	if stop {
		return nil
	}
	return io.ErrUnexpectedEOF
}
