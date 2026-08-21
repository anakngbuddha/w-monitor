package collector

import (
	"testing"
	"time"
)

func TestProcessCPUPercent(t *testing.T) {
	tests := []struct {
		name     string
		cur      float64
		prev     float64
		elapsed  time.Duration
		cores    int
		want     float64
		tolerate float64
	}{
		{
			name:    "idle process reports zero",
			cur:     120.0, // lots of lifetime CPU...
			prev:    120.0, // ...but none since the last poll
			elapsed: 10 * time.Second,
			cores:   8,
			want:    0,
		},
		{
			name:    "one core fully consumed on an 8-core box",
			cur:     10.0,
			prev:    0.0,
			elapsed: 10 * time.Second,
			cores:   8,
			want:    12.5,
		},
		{
			name:    "every core saturated",
			cur:     40.0,
			prev:    0.0,
			elapsed: 10 * time.Second,
			cores:   4,
			want:    100,
		},
		{
			name:    "half a core",
			cur:     5.0,
			prev:    0.0,
			elapsed: 10 * time.Second,
			cores:   1,
			want:    50,
		},
		{
			name:    "backwards counter clamps to zero",
			cur:     5.0,
			prev:    9.0,
			elapsed: 10 * time.Second,
			cores:   4,
			want:    0,
		},
		{
			name:    "zero elapsed is not a divide by zero",
			cur:     5.0,
			prev:    0.0,
			elapsed: 0,
			cores:   4,
			want:    0,
		},
		{
			name:    "impossible value clamps to 100",
			cur:     900.0,
			prev:    0.0,
			elapsed: 10 * time.Second,
			cores:   2,
			want:    100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := processCPUPercent(tc.cur, tc.prev, tc.elapsed, tc.cores)
			tol := tc.tolerate
			if tol == 0 {
				tol = 0.001
			}
			if diff := got - tc.want; diff > tol || diff < -tol {
				t.Errorf("processCPUPercent() = %.4f, want %.4f", got, tc.want)
			}
		})
	}
}

// The whole point of B1: a process with a large lifetime CPU total that is
// currently doing nothing must not outrank a process that is currently busy.
func TestProcessCPUPercentIgnoresLifetimeHistory(t *testing.T) {
	elapsed := 10 * time.Second
	cores := 4

	// Ran hot for an hour at boot, idle ever since.
	oldHeavyIdleNow := processCPUPercent(3600, 3600, elapsed, cores)
	// Just started, currently using a full core.
	youngBusyNow := processCPUPercent(10, 0, elapsed, cores)

	if oldHeavyIdleNow != 0 {
		t.Errorf("idle process reported %.2f%%, want 0", oldHeavyIdleNow)
	}
	if youngBusyNow <= oldHeavyIdleNow {
		t.Errorf("busy process (%.2f%%) must rank above idle process (%.2f%%)", youngBusyNow, oldHeavyIdleNow)
	}
}

func TestProcKeyDistinguishesReusedPIDs(t *testing.T) {
	s := newProcCPUSampler(4)

	original := procKey{pid: 4242, createTime: 1_000_000}
	reused := procKey{pid: 4242, createTime: 9_000_000}

	if original == reused {
		t.Fatal("same PID with different create times must not collide")
	}

	s.prev[original] = procSample{cpuSeconds: 500, sampledAt: time.Now()}
	if _, found := s.prev[reused]; found {
		t.Error("reused PID must not inherit the dead process's CPU baseline")
	}
}

func TestNewProcCPUSamplerClampsCoreCount(t *testing.T) {
	if got := newProcCPUSampler(0).numCores; got != 1 {
		t.Errorf("numCores = %d, want 1 (guards against divide-by-zero)", got)
	}
	if got := newProcCPUSampler(-3).numCores; got != 1 {
		t.Errorf("numCores = %d, want 1", got)
	}
}
