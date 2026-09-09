package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"

	"Zeus/storage"
)

// DrainBatches acknowledges a group only after the whole callback succeeds.
// It uses the same immutable segments and checkpoints as single-entry Drain.
// An interrupted callback can replay a group, which is safe only because every
// event has a durable Hub deduplication identity.
func (s *Spool) DrainBatches(ctx context.Context, maximum int, deliver func([]spoolEntry) error) (int, error) {
	if maximum < 1 || maximum > storage.MaxBatchEvents {
		return 0, errors.New("spool: invalid batch size")
	}
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
		if err := ctx.Err(); err != nil {
			return delivered, err
		}
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
			return delivered, errors.New("spool: corrupt evidence retained in quarantine")
		}
		next, err := checkpoint(path, len(entries))
		if err != nil {
			return delivered, err
		}
		for next < len(entries) {
			if err := ctx.Err(); err != nil {
				return delivered, err
			}
			end, size := next, 1024
			for end < len(entries) && end-next < maximum {
				// Re-encoding legacy rows can expand escaped names and adds event
				// identity fields. A conservative 2x bound leaves envelope room.
				encoded, err := json.Marshal(entries[end])
				if err != nil {
					return delivered, err
				}
				cost := len(encoded)*2 + 1024
				if size+cost > storage.MaxBatchBytes {
					break
				}
				size += cost
				end++
			}
			if end == next {
				return delivered, errors.New("spool: individual event exceeds transport budget; operator review required")
			}
			if err := deliver(entries[next:end]); err != nil {
				return delivered, err
			}
			if err := spoolReplace(path+".ack", []byte(strconv.Itoa(end))); err != nil {
				return delivered, err
			}
			delivered += end - next
			next = end
		}
		s.mu.Lock()
		err = os.Remove(path)
		if err == nil {
			err = syncSpoolDir(s.dir)
		}
		if err == nil {
			if e := os.Remove(path + ".ack"); e != nil && !os.IsNotExist(e) {
				err = e
			}
		}
		s.mu.Unlock()
		if err != nil {
			return delivered, err
		}
	}
	return delivered, nil
}
