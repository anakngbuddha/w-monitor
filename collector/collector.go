// Package collector polls system metrics via gopsutil every 10 seconds
// and writes them into the sysmon SQLite database.
package collector

import (
	"context"
	"log"
	"time"

	"Zeus/storage"

	gopsutil_cpu "github.com/shirou/gopsutil/v3/cpu"
	gopsutil_disk "github.com/shirou/gopsutil/v3/disk"
	gopsutil_mem "github.com/shirou/gopsutil/v3/mem"
	gopsutil_net "github.com/shirou/gopsutil/v3/net"
	gopsutil_proc "github.com/shirou/gopsutil/v3/process"
)

const pollInterval = 10 * time.Second

// Collector gathers system metrics on a fixed interval and writes to the DB.
type Collector struct {
	db       *storage.DB
	interval time.Duration
}

// New creates a Collector with the default poll interval.
func New(db *storage.DB) *Collector {
	return &Collector{db: db, interval: pollInterval}
}

// NewWithInterval creates a Collector with a custom poll interval (useful for testing).
func NewWithInterval(db *storage.DB, interval time.Duration) *Collector {
	return &Collector{db: db, interval: interval}
}

// Run starts the polling loop. It blocks until ctx is cancelled.
func (c *Collector) Run(ctx context.Context) {
	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	log.Printf("[collector] started, interval=%s", c.interval)

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

	// CPU — percent over 1-second sample window (non-blocking on first call;
	// gopsutil returns empty slice on first call on some platforms, so we default 0).
	cpuPcts, err := gopsutil_cpu.Percent(time.Second, false) // false = aggregate
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
		// On Windows the root is C:\
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

	// Network — aggregate all interfaces
	netCounters, err := gopsutil_net.IOCounters(false) // false = aggregate
	if err != nil {
		log.Printf("[collector] net error: %v", err)
	}
	var netSent, netRecv uint64
	if len(netCounters) > 0 {
		netSent = netCounters[0].BytesSent
		netRecv = netCounters[0].BytesRecv
	}

	// Write metric row
	metric := storage.MetricRow{
		Timestamp:    now,
		CPUPct:       cpuPct,
		MemPct:       memPct,
		DiskFreeGB:   diskFreeGB,
		NetSentBytes: netSent,
		NetRecvBytes: netRecv,
		CPUCores:     cpuCores,
		MemTotalGB:   memTotalGB,
		DiskTotalGB:  diskTotalGB,
	}
	if err := c.db.InsertMetric(metric); err != nil {
		return err
	}

	// Processes — top 20 by CPU
	procs, err := collectTopProcesses(now, 20)
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
func collectTopProcesses(ts time.Time, n int) ([]storage.ProcessRow, error) {
	procs, err := gopsutil_proc.Processes()
	if err != nil {
		return nil, err
	}

	type entry struct {
		row storage.ProcessRow
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
