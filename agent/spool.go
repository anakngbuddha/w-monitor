package agent

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Spool bounds. A 100 MB cap holds roughly two weeks of one agent's samples,
// which is far longer than any plausible hub outage, while still guaranteeing a
// permanently unreachable hub cannot fill the client's disk.
const (
	maxSpoolBytes   = 100 << 20 // 100 MB
	maxSegmentBytes = 8 << 20   // 8 MB per segment
	spoolDirName    = "spool"
)

// spoolEntry is one queued payload awaiting delivery.
type spoolEntry struct {
	PayloadType string          `json:"type"`
	Body        json.RawMessage `json:"body"`
	QueuedAt    int64           `json:"queued_at"`
}

// Spool is an append-only, size-bounded, on-disk queue.
//
// Rotating segments rather than using a single file means draining can delete
// completed work by unlinking a whole file, instead of rewriting a large file to
// remove its first line (which would be O(n) per delivered row).
type Spool struct {
	mu      sync.Mutex
	dir     string
	current *os.File
	curSize int64
}

// NewSpool opens (creating if needed) a spool directory.
func NewSpool(dir string) (*Spool, error) {
	path := filepath.Join(dir, spoolDirName)
	if err := os.MkdirAll(path, 0o700); err != nil {
		return nil, fmt.Errorf("spool: create %s: %w", path, err)
	}
	return &Spool{dir: path}, nil
}

// Append queues a payload for later delivery.
func (s *Spool) Append(payloadType string, body []byte) error {
	entry := spoolEntry{
		PayloadType: payloadType,
		Body:        json.RawMessage(body),
		QueuedAt:    time.Now().Unix(),
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("spool: marshal entry: %w", err)
	}
	line = append(line, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.evictIfFullLocked(int64(len(line))); err != nil {
		return err
	}
	if err := s.ensureSegmentLocked(int64(len(line))); err != nil {
		return err
	}

	n, err := s.current.Write(line)
	s.curSize += int64(n)
	if err != nil {
		return fmt.Errorf("spool: write: %w", err)
	}
	return s.current.Sync()
}

// ensureSegmentLocked opens a new segment when there is none or the current one
// is full.
func (s *Spool) ensureSegmentLocked(incoming int64) error {
	if s.current != nil && s.curSize+incoming <= maxSegmentBytes {
		return nil
	}
	if s.current != nil {
		s.current.Close()
		s.current = nil
		s.curSize = 0
	}

	// Nanosecond-precision name keeps segments lexically sortable by age.
	name := fmt.Sprintf("seg-%d.ndjson", time.Now().UnixNano())
	f, err := os.OpenFile(filepath.Join(s.dir, name), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("spool: open segment: %w", err)
	}
	s.current = f
	s.curSize = 0
	return nil
}

// evictIfFullLocked drops the oldest segments until the incoming write fits.
//
// Dropping the oldest data is the right trade: recent metrics are what an
// operator needs when they come back to a recovered agent, and unbounded growth
// would eventually take the monitored machine down, which is the opposite of
// what a monitoring agent should do.
func (s *Spool) evictIfFullLocked(incoming int64) error {
	segments, total, err := s.segmentsLocked()
	if err != nil {
		return err
	}
	for total+incoming > maxSpoolBytes && len(segments) > 0 {
		oldest := segments[0]
		info, statErr := os.Stat(oldest)
		if statErr == nil {
			total -= info.Size()
		}
		if s.current != nil && oldest == s.current.Name() {
			s.current.Close()
			s.current = nil
			s.curSize = 0
		}
		if err := os.Remove(oldest); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("spool: evict %s: %w", oldest, err)
		}
		log.Printf("[spool] evicted oldest segment %s to stay under the size cap", filepath.Base(oldest))
		segments = segments[1:]
	}
	return nil
}

// segmentsLocked lists segment paths oldest first, with their total size.
func (s *Spool) segmentsLocked() ([]string, int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, fmt.Errorf("spool: read dir: %w", err)
	}
	var paths []string
	var total int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".ndjson" {
			continue
		}
		paths = append(paths, filepath.Join(s.dir, e.Name()))
		if info, err := e.Info(); err == nil {
			total += info.Size()
		}
	}
	sort.Strings(paths) // timestamped names sort oldest first
	return paths, total, nil
}

// Depth returns the number of queued entries. Reported on /api/health so a
// backlog is visible rather than silent.
func (s *Spool) Depth() (int, error) {
	s.mu.Lock()
	segments, _, err := s.segmentsLocked()
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}

	count := 0
	for _, path := range segments {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			if len(scanner.Bytes()) > 0 {
				count++
			}
		}
		f.Close()
	}
	return count, nil
}

// SizeBytes reports the spool's on-disk footprint.
func (s *Spool) SizeBytes() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, total, err := s.segmentsLocked()
	return total, err
}

// Close releases any open file handles held by the spool.
func (s *Spool) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.current != nil {
		err := s.current.Close()
		s.current = nil
		s.curSize = 0
		return err
	}
	return nil
}

// Drain delivers queued entries oldest first, stopping at the first failure.
//
// Stopping rather than skipping preserves ordering and prevents a persistent
// failure from burning through the whole backlog against a hub that is still
// down. A partially delivered segment is rewritten with only its undelivered
// remainder.
func (s *Spool) Drain(deliver func(payloadType string, body []byte) error) (delivered int, err error) {
	s.mu.Lock()
	segments, _, listErr := s.segmentsLocked()
	currentName := ""
	if s.current != nil {
		currentName = s.current.Name()
	}
	s.mu.Unlock()
	if listErr != nil {
		return 0, listErr
	}

	for _, path := range segments {
		// Don't drain the segment still being appended to unless it is the only
		// one, to avoid competing with in-flight writes.
		if path == currentName && len(segments) > 1 {
			continue
		}

		entries, readErr := readSegment(path)
		if readErr != nil {
			log.Printf("[spool] unreadable segment %s (%v); discarding it", filepath.Base(path), readErr)
			os.Remove(path)
			continue
		}

		for i, entry := range entries {
			if deliverErr := deliver(entry.PayloadType, entry.Body); deliverErr != nil {
				// Keep everything from this entry onward.
				s.mu.Lock()
				if s.current != nil && s.current.Name() == path {
					s.current.Close()
					s.current = nil
					s.curSize = 0
				}
				s.mu.Unlock()
				if rewriteErr := rewriteSegment(path, entries[i:]); rewriteErr != nil {
					log.Printf("[spool] rewrite %s: %v", filepath.Base(path), rewriteErr)
				}
				return delivered, deliverErr
			}
			delivered++
		}

		s.mu.Lock()
		if s.current != nil && s.current.Name() == path {
			s.current.Close()
			s.current = nil
			s.curSize = 0
		}
		s.mu.Unlock()
		os.Remove(path)
	}
	return delivered, nil
}

func readSegment(path string) ([]spoolEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var entries []spoolEntry
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var entry spoolEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			// A torn final line from an unclean shutdown: skip it rather than
			// discarding every valid entry in the segment.
			continue
		}
		entries = append(entries, entry)
	}
	return entries, scanner.Err()
}

// rewriteSegment atomically replaces a segment with the given entries.
func rewriteSegment(path string, entries []spoolEntry) error {
	if len(entries) == 0 {
		return os.Remove(path)
	}

	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	for _, entry := range entries {
		line, err := json.Marshal(entry)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Sync()
	f.Close()
	_ = os.Remove(path)
	return os.Rename(tmp, path)
}
