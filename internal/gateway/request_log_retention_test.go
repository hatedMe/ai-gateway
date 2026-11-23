package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestLogRetention(t *testing.T) {
	dir := t.TempDir()
	id := "req_" + strings.Repeat("a", 32)
	old := filepath.Join(dir, id+"_inbound.http")
	if err := os.WriteFile(old, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(-2 * time.Hour)
	_ = os.Chtimes(old, stamp, stamp)
	unknown := filepath.Join(dir, "unrelated.txt")
	_ = os.WriteFile(unknown, []byte("keep"), 0600)
	store, err := newRequestLogStore(dir, 20, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	newID := "req_" + strings.Repeat("b", 32)
	if err := store.write(newID, map[string][]byte{"inbound.http": []byte("1234567890"), "inbound.json": []byte("1234567890")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Fatal("expired request retained")
	}
	if _, err := os.Stat(unknown); err != nil {
		t.Fatal("unrelated file removed")
	}
	if err := store.write("req_"+strings.Repeat("c", 32), map[string][]byte{"inbound.http": []byte("oversized request body")}); err == nil {
		t.Fatal("oversized request logged")
	}
	if err := store.write("req_"+strings.Repeat("d", 32), map[string][]byte{"inbound.http": []byte("next")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, newID+"_inbound.http")); !os.IsNotExist(err) {
		t.Fatal("capacity exceeded without eviction")
	}
}

func TestRequestLogConcurrentCapacity(t *testing.T) {
	dir := t.TempDir()
	store, err := newRequestLogStore(dir, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := store.write(fmt.Sprintf("req_%032x", i), map[string][]byte{"inbound.http": []byte("1234567890")}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	entries, _ := os.ReadDir(dir)
	var total int64
	for _, e := range entries {
		info, _ := e.Info()
		total += info.Size()
	}
	if total > 100 {
		t.Fatal("concurrent writes exceeded capacity", total)
	}
}

func TestRequestLogFailureDoesNotRetainEmptyGroups(t *testing.T) {
	dir := t.TempDir()
	store, err := newRequestLogStore(dir, 100, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		id := fmt.Sprintf("req_%032x", i)
		if err := os.Mkdir(filepath.Join(dir, id+"_inbound.http"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := store.write(id, map[string][]byte{"inbound.http": []byte("data")}); err == nil {
			t.Fatal("unexpected write success")
		}
	}
	if len(store.groups) != 0 || len(store.order) != 0 {
		t.Fatalf("failed writes retained groups=%d order=%d", len(store.groups), len(store.order))
	}
}
