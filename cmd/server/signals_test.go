package main

import (
	"os"
	"testing"
)

func TestTerminationSignalsContainsInterrupt(t *testing.T) {
	for _, signal := range terminationSignals() {
		if signal == os.Interrupt {
			return
		}
	}
	t.Fatal("interrupt does not trigger graceful shutdown")
}
