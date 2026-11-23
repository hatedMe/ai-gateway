package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"sync"
	"time"
)

var requestLogName = regexp.MustCompile(`^(req_[a-f0-9]{32})_(inbound|upstream)\.(json|http)$`)

type logGroup struct {
	id    string
	time  time.Time
	files map[string]int64
	bytes int64
}
type requestLogStore struct {
	mu         sync.Mutex
	dir        string
	max, total int64
	retention  time.Duration
	groups     map[string]*logGroup
	order      []*logGroup
}

func newRequestLogStore(dir string, max int64, retention time.Duration) (*requestLogStore, error) {
	if max == 0 {
		max = 1 << 30
	}
	if retention == 0 {
		retention = 7 * 24 * time.Hour
	}
	s := &requestLogStore{dir: dir, max: max, retention: retention, groups: map[string]*logGroup{}}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		match := requestLogName.FindStringSubmatch(e.Name())
		if match == nil || !e.Type().IsRegular() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		g := s.groups[match[1]]
		if g == nil {
			g = &logGroup{id: match[1], time: info.ModTime(), files: map[string]int64{}}
			s.groups[g.id] = g
			s.order = append(s.order, g)
		}
		if info.ModTime().Before(g.time) {
			g.time = info.ModTime()
		}
		g.files[e.Name()] = info.Size()
		g.bytes += info.Size()
		s.total += info.Size()
	}
	sort.Slice(s.order, func(i, j int) bool { return s.order[i].time.Before(s.order[j].time) })
	return s, nil
}

func (s *requestLogStore) remove(g *logGroup) error {
	for name, size := range g.files {
		path := filepath.Join(s.dir, name)
		info, err := os.Lstat(path)
		if err == nil && !info.Mode().IsRegular() {
			return fmt.Errorf("log file is no longer regular")
		}
		if err != nil && !os.IsNotExist(err) {
			return err
		}
		if err == nil {
			if err = os.Remove(path); err != nil {
				return err
			}
		}
		delete(g.files, name)
		g.bytes -= size
		s.total -= size
	}
	delete(s.groups, g.id)
	return nil
}

// Each call writes a JSON/HTTP pair under one lock; the index is scanned once.
func (s *requestLogStore) write(id string, files map[string][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var size int64
	for suffix, raw := range files {
		if !requestLogName.MatchString(id + "_" + suffix) {
			return fmt.Errorf("invalid log filename")
		}
		size += int64(len(raw))
	}
	if size > s.max {
		return fmt.Errorf("request log skipped: body exceeds capacity")
	}
	g := s.groups[id]
	var replaced int64
	if g != nil {
		for suffix := range files {
			replaced += g.files[id+"_"+suffix]
		}
		if g.bytes+size-replaced > s.max {
			return fmt.Errorf("request log skipped: request group exceeds capacity")
		}
	}
	cutoff := time.Now().Add(-s.retention)
	for len(s.order) > 0 {
		old := s.order[0]
		if s.groups[old.id] != old {
			s.order = s.order[1:]
			continue
		}
		if !old.time.Before(cutoff) && s.total+size-replaced <= s.max {
			break
		}
		if err := s.remove(old); err != nil {
			return err
		}
		s.order = s.order[1:]
		if old == g {
			g = nil
			replaced = 0
		}
	}
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	for suffix, raw := range files {
		name := id + "_" + suffix
		path := filepath.Join(s.dir, name)
		// Exclusive creation also refuses symlinks or external replacements.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		n, writeErr := f.Write(raw)
		closeErr := f.Close()
		if n == 0 && (writeErr != nil || closeErr != nil) {
			_ = os.Remove(path)
			if writeErr != nil {
				return writeErr
			}
			return closeErr
		}
		if g == nil {
			g = &logGroup{id: id, time: time.Now(), files: map[string]int64{}}
			s.groups[id] = g
			s.order = append(s.order, g)
		}
		g.files[name] = int64(n)
		g.bytes += int64(n)
		s.total += int64(n)
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
