// Package collector polls system metrics via gopsutil every 10 seconds
// and writes them into the storage backend.
package collector

import (
	"context"
	"log"
	"os"
	"time"

	"Zeus/storage"

	gopsutil_cpu "github.com/shirou/gopsutil/v3/cpu"
	gopsutil_disk "github.com/shirou/gopsutil/v3/disk"
	gopsutil_mem "github.com/shirou/gopsutil/v3/mem"
	gopsutil_net "github.com/shirou/gopsutil/v3/net"
	gopsutil_proc "github.com/shirou/gopsutil/v3/process"
)

const pollInterval = 10 * time.Second

// UserTracker provides the active concurrent user count.
type UserTracker interface {
	GetConcurrentUsers() int
}

// Collector gathers system metrics on a fixed interval and writes to the store.
type Collector struct {
	db              storage.Store
	interval        time.Duration
	userTracker     UserTracker
	serverID        string // set via SetServerID
	hostname        string // resolved at startup
	externalIface   string // override for external NIC auto-detect
	prevTime        time.Time
	prevReadOps     uint64
	prevWriteOps    uint64
	prevNetSent     uint64
	prevNetRecv     uint64
	// per-interface previous counters
	prevExtSent uint64
	prevExtRecv uint64
	prevIntSent uint64
	prevIntRecv uint64
}

// New creates a Collector with the default poll interval.
func New(db storage.Store) *Collector {
	h, _ := resolveHostname()
	return &Collector{
		db:          db,
		interval:    pollInterval,
		hostname:    h,
		serverID:    h,
		userTracker: NewTCPUserTracker(),
	}
}

// NewWithInterval creates a Collector with a custom poll interval (useful for testing).
func NewWithInterval(db storage.Store, interval time.Duration) *Collector {
	h, _ := resolveHostname()
	return &Collector{
		db:          db,
		interval:    interval,
		hostname:    h,
		serverID:    h,
		userTracker: NewTCPUserTracker(),
	}
}

// UserTracker returns the current UserTracker instance.
func (c *Collector) UserTracker() UserTracker {
	return c.userTracker
}

// SetUserTracker sets the UserTracker instance.
func (c *Collector) SetUserTracker(ut UserTracker) {
	c.userTracker = ut
}

// SetServerID sets the server_id tag written with every metric row.
func (c *Collector) SetServerID(id string) {
	c.serverID = id
}

// SetExternalIface sets the NIC name to treat as external (overrides auto-detect).
func (c *Collector) SetExternalIface(iface string) {
	c.externalIface = iface
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	log.Printf("[collector] started, interval=%s server_id=%q", c.interval, c.serverID)

	for {
		select {
		case <-ctx.Done():
			log.Println("[collector] stopping")
			return
		case <-ticker.C:
			if err := c.collect(); err != nil {
				log.Printf("[collector] collect error: %v", err)
			}
		}
	}
}

// collect gathers one snapshot and writes it to the database.
func (c *Collector) collect() error {
	now := time.Now()

	// CPU — percent over 1-second sample window
	cpuPcts, err := gopsutil_cpu.Percent(time.Second, false)
	if err != nil {
		log.Printf("[collector] cpu error: %v", err)
	}
	cpuPct := 0.0
	if len(cpuPcts) > 0 {
		cpuPct = cpuPcts[0]
	}

	cpuCores, err := gopsutil_cpu.Counts(true)
	if err != nil {
		log.Printf("[collector] cpu cores error: %v", err)
	}

	// Memory
	vm, err := gopsutil_mem.VirtualMemory()
	if err != nil {
		log.Printf("[collector] mem error: %v", err)
	}
	memPct := 0.0
	memTotalGB := 0.0
	if vm != nil {
		memPct = vm.UsedPercent
		memTotalGB = float64(vm.Total) / (1024 * 1024 * 1024)
	}

	// Disk — root partition
	diskPath := "/"
	disk, err := gopsutil_disk.Usage(diskPath)
	if err != nil {
		disk, err = gopsutil_disk.Usage("C:\\")
		if err != nil {
			log.Printf("[collector] disk error: %v", err)
		}
	}
	diskFreeGB := 0.0
	diskTotalGB := 0.0
	if disk != nil {
		diskFreeGB = float64(disk.Free) / (1024 * 1024 * 1024)
		diskTotalGB = float64(disk.Total) / (1024 * 1024 * 1024)
	}

	// Disk IOPS counters
	var diskReadOps, diskWriteOps uint64
	diskCounters, err := gopsutil_disk.IOCounters()
	if err != nil {
		log.Printf("[collector] disk io error: %v", err)
	} else {
		for _, d := range diskCounters {
			diskReadOps += d.ReadCount
			diskWriteOps += d.WriteCount
		}
	}

	// Network — per-interface for traffic split (Phase 10)
	var netSent, netRecv uint64
	var extSent, extRecv, intSent, intRecv uint64

	ifaceCounters, err := gopsutil_net.IOCounters(true) // true = per-interface
	if err != nil {
		log.Printf("[collector] net per-iface error: %v", err)
	} else {
		externalIface := c.externalIface
		if externalIface == "" {
			externalIface = detectExternalIface()
		}
		for _, ifc := range ifaceCounters {
			netSent += ifc.BytesSent
			netRecv += ifc.BytesRecv
			if ifc.Name == externalIface {
				extSent = ifc.BytesSent
				extRecv = ifc.BytesRecv
			} else {
				intSent += ifc.BytesSent
				intRecv += ifc.BytesRecv
			}
		}
	}

	// Calculate IOPS and Net MB/s deltas
	diskIOPS := 0.0
	netMBps := 0.0
	if !c.prevTime.IsZero() {
		elapsed := now.Sub(c.prevTime).Seconds()
		if elapsed > 0 {
			var deltaRead, deltaWrite uint64
			if diskReadOps >= c.prevReadOps {
				deltaRead = diskReadOps - c.prevReadOps
			} else {
				deltaRead = diskReadOps
			}
			if diskWriteOps >= c.prevWriteOps {
				deltaWrite = diskWriteOps - c.prevWriteOps
			} else {
				deltaWrite = diskWriteOps
			}
			diskIOPS = float64(deltaRead+deltaWrite) / elapsed

			var deltaSent, deltaRecv uint64
			if netSent >= c.prevNetSent {
				deltaSent = netSent - c.prevNetSent
			} else {
				deltaSent = netSent
			}
			if netRecv >= c.prevNetRecv {
				deltaRecv = netRecv - c.prevNetRecv
			} else {
				deltaRecv = netRecv
			}
			netMBps = float64(deltaSent+deltaRecv) / (elapsed * 1024 * 1024)
		}
	}
	c.prevTime = now
	c.prevReadOps = diskReadOps
	c.prevWriteOps = diskWriteOps
	c.prevNetSent = netSent
	c.prevNetRecv = netRecv
	c.prevExtSent = extSent
	c.prevExtRecv = extRecv
	c.prevIntSent = intSent
	c.prevIntRecv = intRecv

	// Concurrent Users
	concurrentUsers := 0
	if c.userTracker != nil {
		concurrentUsers = c.userTracker.GetConcurrentUsers()
	}

	// Write metric row
	metric := storage.MetricRow{
		Timestamp:       now,
		ServerID:        c.serverID,
		Hostname:        c.hostname,
		CPUPct:          cpuPct,
		MemPct:          memPct,
		DiskFreeGB:      diskFreeGB,
		NetSentBytes:    netSent,
		NetRecvBytes:    netRecv,
		CPUCores:        cpuCores,
		MemTotalGB:      memTotalGB,
		DiskTotalGB:     diskTotalGB,
		DiskReadOps:     diskReadOps,
		DiskWriteOps:    diskWriteOps,
		DiskIOPS:        diskIOPS,
		NetMBps:         netMBps,
		ConcurrentUsers: concurrentUsers,
		NetSentExternal: extSent,
		NetRecvExternal: extRecv,
		NetSentInternal: intSent,
		NetRecvInternal: intRecv,
	}
	if err := c.db.InsertMetric(metric); err != nil {
		return err
	}

	// Processes — top 20 by CPU
	procs, err := collectTopProcesses(now, c.serverID, c.hostname, 20)
	if err != nil {
		log.Printf("[collector] processes error: %v", err)
	} else {
		for _, p := range procs {
			if err := c.db.InsertProcess(p); err != nil {
				log.Printf("[collector] insert process error: %v", err)
			}
		}
	}

	log.Printf("[collector] cpu=%.1f%% mem=%.1f%% disk_free=%.1fGB net_sent=%d net_recv=%d procs=%d",
		cpuPct, memPct, diskFreeGB, netSent, netRecv, len(procs))
	return nil
}

// collectTopProcesses returns process stats for the top N processes by CPU.
func collectTopProcesses(ts time.Time, serverID, hostname string, n int) ([]storage.ProcessRow, error) {
	procs, err := gopsutil_proc.Processes()
	if err != nil {
		return nil, err
	}

	var rows []storage.ProcessRow

	for _, p := range procs {
		name, _ := p.Name()
		cpu, err := p.CPUPercent()
		if err != nil {
			continue
		}
		memInfo, err := p.MemoryInfo()
		if err != nil || memInfo == nil {
			continue
		}
		memMB := float64(memInfo.RSS) / (1024 * 1024)
		rows = append(rows, storage.ProcessRow{
			Timestamp: ts,
			ServerID:  serverID,
			Hostname:  hostname,
			PID:       p.Pid,
			Name:      name,
			CPUPct:    cpu,
			MemMB:     memMB,
		})
	}

	// Sort by CPU descending (simple selection of top N)
	for i := 0; i < len(rows) && i < n; i++ {
		max := i
		for j := i + 1; j < len(rows); j++ {
			if rows[j].CPUPct > rows[max].CPUPct {
				max = j
			}
		}
		rows[i], rows[max] = rows[max], rows[i]
	}

	if len(rows) > n {
		rows = rows[:n]
	}
	return rows, nil
}

// resolveHostname returns the system hostname.
func resolveHostname() (string, error) {
	h, err := os.Hostname()
	if err == nil && h != "" {
		return h, nil
	}
	return "localhost", nil
}

// detectExternalIface returns the NIC name that holds the default route,
// used as the "external" interface for traffic split. Falls back to "" on error.
func detectExternalIface() string {
	// Use the first non-loopback interface that has a gateway — heuristic:
	// The interface with the most bytes sent is typically the external one.
	// For a proper implementation we would inspect routing tables, but this
	// is platform-specific. We use a simple heuristic that works in most cases.
	ifaces, err := gopsutil_net.IOCounters(true)
	if err != nil || len(ifaces) == 0 {
		return ""
	}

	var bestName string
	var bestBytes uint64
	for _, ifc := range ifaces {
		// Skip loopback
		if ifc.Name == "lo" || ifc.Name == "Loopback Pseudo-Interface 1" {
			continue
		}
		total := ifc.BytesSent + ifc.BytesRecv
		if total > bestBytes {
			bestBytes = total
			bestName = ifc.Name
		}
	}
	return bestName
}
