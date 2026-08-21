package collector

import (
	"log"
	"sort"
	"time"

	"Zeus/storage"

	gopsutil_proc "github.com/shirou/gopsutil/v3/process"
)

// procKey identifies a process across polls.
//
// PID alone is not enough: operating systems reuse PIDs, and a reused PID would
// be diffed against the CPU time of a completely unrelated dead process,
// producing a garbage (often negative, hence clamped) delta. Pairing the PID
// with its creation timestamp makes the identity stable.
type procKey struct {
	pid        int32
	createTime int64
}

// procSample is the previous CPU reading for one process.
type procSample struct {
	cpuSeconds float64
	sampledAt  time.Time
}

// procCPUSampler holds per-process CPU baselines between polls.
type procCPUSampler struct {
	prev     map[procKey]procSample
	numCores int
}

func newProcCPUSampler(numCores int) *procCPUSampler {
	if numCores < 1 {
		numCores = 1
	}
	return &procCPUSampler{
		prev:     make(map[procKey]procSample),
		numCores: numCores,
	}
}

// processCPUPercent converts two CPU-time readings into a percentage of total
// machine CPU capacity consumed over the interval between them.
//
// This is the calculation gopsutil's Process.CPUPercent() does NOT do. That
// method divides cumulative CPU time by the process's entire lifetime, so a
// process that pinned a core for an hour at boot and has idled ever since still
// reports a high number forever. Ranking "top processes by CPU" with it means
// ranking by lifetime average, not by current load, which is why long-running
// services always floated to the top of the list.
func processCPUPercent(curCPUSeconds, prevCPUSeconds float64, elapsed time.Duration, numCores int) float64 {
	if elapsed <= 0 || numCores < 1 {
		return 0
	}
	deltaCPU := curCPUSeconds - prevCPUSeconds
	if deltaCPU < 0 {
		// Counter went backwards: treat as unknown rather than negative load.
		return 0
	}
	pct := (deltaCPU / (elapsed.Seconds() * float64(numCores))) * 100
	if pct < 0 {
		return 0
	}
	// A process cannot exceed the whole machine. Clamp to absorb scheduler
	// accounting jitter on very short intervals.
	if pct > 100 {
		pct = 100
	}
	return pct
}

// sample walks the process table and returns the top n processes by CPU used
// during the interval since the previous call.
//
// Processes seen for the first time are recorded but not returned: without a
// baseline there is no honest delta to report, and reporting 0 would rank them
// misleadingly low while reporting lifetime-average would be the bug we are
// fixing. They appear from the following tick onward.
func (s *procCPUSampler) sample(ts time.Time, serverID, hostname string, n int) ([]storage.ProcessRow, error) {
	procs, err := gopsutil_proc.Processes()
	if err != nil {
		return nil, err
	}

	current := make(map[procKey]procSample, len(procs))
	rows := make([]storage.ProcessRow, 0, len(procs))

	for _, p := range procs {
		times, err := p.Times()
		if err != nil || times == nil {
			continue
		}
		// User + System is the process's own consumed CPU. Deliberately not
		// TimesStat.Total(), which folds in idle/steal on some platforms.
		cpuSeconds := times.User + times.System

		createTime, err := p.CreateTime()
		if err != nil {
			createTime = 0
		}
		key := procKey{pid: p.Pid, createTime: createTime}
		current[key] = procSample{cpuSeconds: cpuSeconds, sampledAt: ts}

		prev, seen := s.prev[key]
		if !seen {
			continue // no baseline yet
		}

		memInfo, err := p.MemoryInfo()
		if err != nil || memInfo == nil {
			continue
		}
		name, _ := p.Name()

		rows = append(rows, storage.ProcessRow{
			Timestamp: ts,
			ServerID:  serverID,
			Hostname:  hostname,
			PID:       p.Pid,
			Name:      name,
			CPUPct:    processCPUPercent(cpuSeconds, prev.cpuSeconds, ts.Sub(prev.sampledAt), s.numCores),
			MemMB:     float64(memInfo.RSS) / (1024 * 1024),
		})
	}

	// Replacing the map wholesale also prunes processes that have exited, so the
	// baseline map cannot grow without bound on a host with heavy process churn.
	s.prev = current

	// Sort AFTER deltas are computed. The old code ran a partial selection sort
	// over lifetime-average values, so it was sorting the wrong numbers.
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].CPUPct != rows[j].CPUPct {
			return rows[i].CPUPct > rows[j].CPUPct
		}
		// Stable tiebreak: at idle almost everything is 0% CPU, so without this
		// the persisted top-20 would shuffle randomly between ticks.
		return rows[i].MemMB > rows[j].MemMB
	})

	if len(rows) > n {
		rows = rows[:n]
	}
	return rows, nil
}

// prime records an initial CPU baseline for every process so that the first
// real collection has deltas to work with.
func (s *procCPUSampler) prime() {
	if _, err := s.sample(time.Now(), "", "", 0); err != nil {
		log.Printf("[collector] process baseline priming failed: %v", err)
	}
}
