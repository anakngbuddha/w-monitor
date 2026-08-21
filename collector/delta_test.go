package collector

import "testing"

func TestCounterDelta(t *testing.T) {
	tests := []struct {
		name      string
		cur, prev uint64
		want      uint64
		wantReset bool
	}{
		{"normal increase", 1000, 400, 600, false},
		{"no change", 500, 500, 0, false},
		{"first sample from zero", 250, 0, 250, false},
		{"counter reset to zero", 0, 9_000_000_000, 0, true},
		{"counter reset partway", 120, 9_000_000_000, 0, true},
		{"large but valid", 18_000_000_000_000, 17_999_999_999_000, 1000, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, reset := counterDelta(tc.cur, tc.prev)
			if got != tc.want {
				t.Errorf("delta = %d, want %d", got, tc.want)
			}
			if reset != tc.wantReset {
				t.Errorf("reset = %v, want %v", reset, tc.wantReset)
			}
		})
	}
}

// A reset must never be reported as throughput. This is the regression that
// produced multi-GB/s spikes in the assessment report after a reboot.
func TestCounterDeltaNeverFabricatesSpike(t *testing.T) {
	const sinceBoot = 8_000_000_000 // 8 GB moved before the reset
	delta, reset := counterDelta(0, sinceBoot)
	if !reset {
		t.Fatal("expected reset to be detected")
	}
	if delta != 0 {
		t.Fatalf("reset produced a delta of %d bytes; must be 0", delta)
	}
}
