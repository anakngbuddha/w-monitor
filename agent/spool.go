package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"Zeus/internal/fsroot"
)

const (
	maxSpoolBytes   = 100 << 20
	maxSegmentBytes = 8 << 20
	spoolDirName    = "spool"
	spoolBindName   = "bind.json"
)

var ErrSpoolFull = errors.New("spool: disk budget exhausted; sample not queued")

type spoolBinding struct {
	Origin   string `json:"origin"`
	TenantID string `json:"tenant_id,omitempty"`
	ServerID string `json:"server_id,omitempty"`
}

type spoolEntry struct {
	PayloadType string          `json:"type"`
	Body        json.RawMessage `json:"body"`
	QueuedAt    int64           `json:"queued_at"`
}

// drainMu serializes draining, rebinding and closing. mu only guards short
// filesystem operations: append never shares a segment with network delivery.
// The OS lock is released on process death, not by a guessed stale-lock timer.
// Corrupt/old-origin data is retained in quarantine and counts toward the cap.
// Checkpoints provide crash recovery; Hub event deduplication is still required
// to resolve a successful commit whose acknowledgement was lost.
type Spool struct {
	mu      sync.Mutex
	drainMu sync.Mutex
	dir     string
	current *os.File
	curSize int64
	lock    *os.File
	closed  bool
}

func NewSpool(dir string) (*Spool, error) {
	if fsroot.IsolationEnabled() {
		if err := fsroot.RejectProductionPath(dir); err != nil {
			return nil, fmt.Errorf("spool: %w", err)
		}
	}
	path := filepath.Join(dir, spoolDirName)
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return nil, err
	}
	if err := secureSpoolDirectory(path); err != nil {
		return nil, err
	}
	lock, err := lockSpool(filepath.Join(path, "owner.lock"))
	if err != nil {
		return nil, fmt.Errorf("spool: another process owns the queue or ownership could not be established: %w", err)
	}
	return &Spool{dir: path, lock: lock}, nil
}

func (s *Spool) sealLocked() error {
	if s.current == nil {
		return nil
	}
	if err := s.current.Sync(); err != nil {
		return err
	}
	err := s.current.Close()
	s.current = nil
	s.curSize = 0
	return err
}

func (s *Spool) BindDestination(origin, tenantID, serverID string) error {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	origin = CanonicalHubURL(origin)
	if origin == "" {
		return errors.New("spool: canonical destination is required")
	}
	existing, err := s.readBindLocked()
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	mismatch := existing != nil && ((existing.Origin != "" && !SameHubOrigin(existing.Origin, origin)) || (existing.TenantID != "" && tenantID != "" && existing.TenantID != tenantID) || (existing.ServerID != "" && serverID != "" && existing.ServerID != serverID))
	if mismatch {
		if err := s.quarantineLocked(); err != nil {
			return err
		}
		existing = nil
	}
	binding := spoolBinding{Origin: origin, TenantID: tenantID, ServerID: serverID}
	if existing != nil {
		if binding.TenantID == "" {
			binding.TenantID = existing.TenantID
		}
		if binding.ServerID == "" {
			binding.ServerID = existing.ServerID
		}
	}
	return s.writeBindLocked(binding)
}

func (s *Spool) bindPath() string { return filepath.Join(s.dir, spoolBindName) }

func (s *Spool) readBindLocked() (*spoolBinding, error) {
	if err := rejectSymlink(s.bindPath()); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.bindPath())
	if err != nil {
		return nil, err
	}
	var binding spoolBinding
	if err := json.Unmarshal(data, &binding); err != nil {
		return nil, errors.New("spool: invalid destination binding; operator review required")
	}
	return &binding, nil
}

func (s *Spool) writeBindLocked(binding spoolBinding) error {
	data, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	return spoolReplace(s.bindPath(), data)
}

func (s *Spool) quarantineLocked() error {
	if err := s.sealLocked(); err != nil {
		return err
	}
	segments, _, err := s.segmentsLocked()
	if err != nil {
		return err
	}
	for _, path := range segments {
		if err := s.quarantinePathLocked(path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Spool) quarantinePathLocked(path string) error {
	qdir := filepath.Join(s.dir, "quarantine")
	if err := os.MkdirAll(qdir, 0700); err != nil {
		return err
	}
	if err := secureSpoolDirectory(qdir); err != nil {
		return err
	}
	if err := os.Rename(path, filepath.Join(qdir, filepath.Base(path))); err != nil {
		return err
	}
	if err := os.Rename(path+".ack", filepath.Join(qdir, filepath.Base(path)+".ack")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncSpoolDir(qdir); err != nil {
		return err
	}
	return syncSpoolDir(s.dir)
}

// RecordLoss stores only bounded reason codes and counts, never payloads or
// credentials. Callers must propagate a recording failure, not claim durability.
func (s *Spool) RecordLoss(reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.recordLossLocked(reason)
}

func (s *Spool) recordLossLocked(reason string) error {
	switch reason {
	case "overflow", "corrupt_segment", "permanent_rejection":
	default:
		return errors.New("spool: unsupported loss reason")
	}
	path := filepath.Join(s.dir, "loss.json")
	counts := map[string]uint64{}
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &counts); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if counts[reason] != ^uint64(0) {
		counts[reason]++
	}
	data, err = json.Marshal(counts)
	if err != nil {
		return err
	}
	return spoolReplace(path, data)
}

func (s *Spool) Append(payloadType string, body []byte) error {
	entry := spoolEntry{PayloadType: payloadType, Body: json.RawMessage(body), QueuedAt: time.Now().Unix()}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if len(line) > 1<<20 {
		return errors.New("spool: entry exceeds 1 MiB")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return os.ErrClosed
	}
	if err := s.evictIfFullLocked(int64(len(line))); err != nil {
		return err
	}
	if err := s.ensureSegmentLocked(int64(len(line))); err != nil {
		return err
	}
	before := s.curSize
	n, err := s.current.Write(line)
	if err != nil || n != len(line) {
		if rollbackErr := s.current.Truncate(before); rollbackErr != nil {
			return fmt.Errorf("spool: partial write could not be rolled back: %w", rollbackErr)
		}
		if err == nil {
			err = io.ErrShortWrite
		}
		return err
	}
	s.curSize += int64(n)
	return s.current.Sync()
}

func (s *Spool) ensureSegmentLocked(incoming int64) error {
	if s.current != nil && s.curSize+incoming <= maxSegmentBytes {
		return nil
	}
	if err := s.sealLocked(); err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, fmt.Sprintf("seg-%020d-*.ndjson", time.Now().UnixNano()))
	if err != nil {
		return err
	}
	if err := f.Chmod(0600); err != nil {
		f.Close()
		return err
	}
	if err := syncSpoolDir(s.dir); err != nil {
		f.Close()
		return err
	}
	s.current, s.curSize = f, 0
	return nil
}

// The former eviction entry point now applies explicit backpressure. It must
// never delete a sealed segment concurrently being delivered.
func (s *Spool) evictIfFullLocked(incoming int64) error {
	var total int64
	err := filepath.WalkDir(s.dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("spool: symlink in queue")
		}
		if !entry.IsDir() {
			info, err := entry.Info()
			if err != nil {
				return err
			}
			total += info.Size()
		}
		return nil
	})
	if err != nil {
		return err
	}
	if incoming > maxSpoolBytes || total > maxSpoolBytes-incoming {
		if err := s.recordLossLocked("overflow"); err != nil {
			return fmt.Errorf("%w; loss counter persistence failed", ErrSpoolFull)
		}
		return ErrSpoolFull
	}
	return nil
}

func (s *Spool) segmentsLocked() ([]string, int64, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, err
	}
	var paths []string
	var total int64
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "seg-") || filepath.Ext(entry.Name()) != ".ndjson" {
			continue
		}
		if !entry.Type().IsRegular() {
			return nil, 0, errors.New("spool: non-regular segment")
		}
		info, err := entry.Info()
		if err != nil {
			return nil, 0, err
		}
		paths = append(paths, filepath.Join(s.dir, entry.Name()))
		total += info.Size()
	}
	sort.Strings(paths)
	return paths, total, nil
}

func checkpoint(path string, maximum int) (int, error) {
	data, err := os.ReadFile(path + ".ack")
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(data))
	if err != nil || n < 0 || n > maximum {
		return 0, errors.New("spool: invalid checkpoint; operator review required")
	}
	return n, nil
}

func (s *Spool) Depth() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	paths, _, err := s.segmentsLocked()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, path := range paths {
		entries, err := readSegment(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return count, err
		}
		n, err := checkpoint(path, len(entries))
		if err != nil {
			return count, err
		}
		count += len(entries) - n
	}
	return count, nil
}

func (s *Spool) SizeBytes() (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, total, err := s.segmentsLocked()
	return total, err
}

func (s *Spool) Close() error {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.sealLocked()
	if s.lock != nil {
		if closeErr := s.lock.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}

func (s *Spool) Drain(deliver func(string, []byte) error) (int, error) {
	s.drainMu.Lock()
	defer s.drainMu.Unlock()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, os.ErrClosed
	}
	if err := s.sealLocked(); err != nil {
		s.mu.Unlock()
		return 0, err
	}
	paths, _, err := s.segmentsLocked()
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	delivered := 0
	for _, path := range paths {
		entries, err := readSegment(path)
		if err != nil {
			s.mu.Lock()
			recordErr := s.recordLossLocked("corrupt_segment")
			if recordErr == nil {
				recordErr = s.quarantinePathLocked(path)
			}
			s.mu.Unlock()
			if recordErr != nil {
				return delivered, recordErr
			}
			return delivered, errors.New("spool: corrupt segment retained in quarantine; operator review required")
		}
		n, err := checkpoint(path, len(entries))
		if err != nil {
			return delivered, err
		}
		for i := n; i < len(entries); i++ {
			if err := deliver(entries[i].PayloadType, entries[i].Body); err != nil {
				return delivered, err
			}
			if err := spoolReplace(path+".ack", []byte(strconv.Itoa(i+1))); err != nil {
				return delivered, err
			}
			delivered++
		}
		s.mu.Lock()
		err = os.Remove(path)
		if err == nil {
			err = syncSpoolDir(s.dir)
		}
		if err == nil {
			if removeErr := os.Remove(path + ".ack"); removeErr != nil && !os.IsNotExist(removeErr) {
				err = removeErr
			}
		}
		s.mu.Unlock()
		if err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}

func readSegment(path string) ([]spoolEntry, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxSegmentBytes+1<<20 {
		return nil, errors.New("spool: oversized segment")
	}
	reader := bufio.NewReaderSize(f, 64<<10)
	var entries []spoolEntry
	for {
		line, err := reader.ReadBytes('\n')
		if err == io.EOF && len(line) == 0 {
			return entries, nil
		}
		if err != nil || len(line) > 1<<20 {
			return nil, errors.New("spool: torn or oversized record")
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var entry spoolEntry
		if err := json.Unmarshal(line, &entry); err != nil || entry.PayloadType == "" || !json.Valid(entry.Body) {
			return nil, errors.New("spool: invalid record")
		}
		entries = append(entries, entry)
	}
}

// Kept for compatibility with focused fixtures; Drain uses checkpoints instead
// of rewriting the immutable source segment after every partial delivery.
func rewriteSegment(path string, entries []spoolEntry) error {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	for _, entry := range entries {
		if err := enc.Encode(entry); err != nil {
			return err
		}
	}
	return spoolReplace(path, body.Bytes())
}
