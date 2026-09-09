package retention

import (
	"context"
	"log"
	"time"
)

// RunContext keeps containment status current without an immortal background
// ticker. The service joins this worker before closing storage.
func (j *Job) RunContext(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := j.Run(); err != nil {
			log.Print("[retention] disk-pressure containment active")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
