package retention

import (
	"fmt"
	"os"
)

// CheckCapacity includes SQLite's WAL/SHM, which can be larger than the base
// file. Filesystem errors fail closed. PostgreSQL capacity must additionally be
// enforced by the database operator; a local file budget cannot measure it.
func (j *Job) CheckCapacity() error {
	j.mu.Lock()
	path, budget := j.dataPath, j.diskBudgetBytes
	j.mu.Unlock()
	status := Status{Reason: DisabledReason, DiskBudgetBytes: budget}
	if path == "" { j.setStatus(status); return nil }
	for index, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(path+suffix)
		if err != nil {
			if index > 0 && os.IsNotExist(err) { continue }
			status.DiskPressure = true
			j.setStatus(status)
			return fmt.Errorf("retention: capacity check unavailable")
		}
		if !info.Mode().IsRegular() { return fmt.Errorf("retention: data path is not a regular file") }
		status.DiskUsedBytes += info.Size()
	}
	status.DiskPressure = budget > 0 && status.DiskUsedBytes >= budget
	j.setStatus(status)
	if status.DiskPressure { return ErrDiskPressure }
	return nil
}
