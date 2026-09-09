package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"time"
)

const (
	IngestSchema   = "zeus.ingest.v1"
	MaxBatchEvents = 64
	MaxBatchBytes  = 256 << 10
	MaxEventAge    = 30 * 24 * time.Hour
)

var (
	ErrEventConflict  = errors.New("ingest: event identity already has different content")
	ErrAcceptedBudget = errors.New("ingest: accepted-data budget exhausted")
	eventIdentifier   = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)
)

type IngestEvent struct {
	EventID  string      `json:"event_id"`
	BootID   string      `json:"boot_id"`
	Sequence uint64      `json:"sequence"`
	Metric   *MetricRow  `json:"metric,omitempty"`
	Process  *ProcessRow `json:"process,omitempty"`
}

type IngestBatch struct {
	SchemaVersion string        `json:"schema_version"`
	Events        []IngestEvent `json:"events"`
}

type IngestOutcome struct {
	EventID string `json:"event_id"`
	Status  string `json:"status"`
}

type IngestPolicy struct {
	DailyRows       int64
	DailyBytes      int64
	AgentDailyRows  int64
	AgentDailyBytes int64
}

func DefaultIngestPolicy() IngestPolicy {
	return IngestPolicy{DailyRows: 20_000_000, DailyBytes: 20 << 30, AgentDailyRows: 250_000, AgentDailyBytes: 256 << 20}
}

func (p IngestPolicy) Validate() error {
	if p.DailyRows < 1 || p.DailyBytes < MaxBatchBytes || p.AgentDailyRows < 1 || p.AgentDailyBytes < MaxBatchBytes || p.AgentDailyRows > p.DailyRows || p.AgentDailyBytes > p.DailyBytes {
		return errors.New("ingest: invalid tenant/agent budget configuration")
	}
	return nil
}

// DecodeIngest rejects unknown fields, duplicate keys, trailing JSON and deep
// nesting before typed decoding. Transport callers must bound input reads too.
func DecodeIngest(body []byte, target any) error {
	if len(body) == 0 || len(body) > MaxBatchBytes {
		return errors.New("ingest: invalid body size")
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := inspectJSONValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("ingest: trailing JSON")
	}
	dec = json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	return dec.Decode(target)
}

func inspectJSONValue(dec *json.Decoder, depth int) error {
	if depth > 16 {
		return errors.New("ingest: JSON nesting limit exceeded")
	}
	token, err := dec.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("ingest: duplicate or invalid object key")
			}
			seen[name] = true
			if err := inspectJSONValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := inspectJSONValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("ingest: unexpected JSON delimiter")
	}
	_, err = dec.Token()
	return err
}

func validMeasurement(values ...float64) bool {
	for _, v := range values {
		if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 1e15 {
			return false
		}
	}
	return true
}

// NormalizeIngest derives ownership from the authenticated principal. Timestamp
// admissibility and event identities are validated before starting any writes.
func NormalizeIngest(batch *IngestBatch, tenant, agent string, now time.Time) error {
	if RequireTenant(tenant) != nil || agent == "" || len(agent) > 256 || batch.SchemaVersion != IngestSchema || len(batch.Events) == 0 || len(batch.Events) > MaxBatchEvents {
		return errors.New("ingest: invalid scope, schema or batch cardinality")
	}
	seen := map[string]bool{}
	for i := range batch.Events {
		e := &batch.Events[i]
		if !eventIdentifier.MatchString(e.EventID) || !eventIdentifier.MatchString(e.BootID) || e.Sequence == 0 || e.Sequence > math.MaxInt64 || seen[e.EventID] || (e.Metric == nil) == (e.Process == nil) {
			return errors.New("ingest: invalid or repeated event identity")
		}
		seen[e.EventID] = true
		var timestamp time.Time
		if m := e.Metric; m != nil {
			if m.ID != 0 || (m.TenantID != "" && m.TenantID != tenant) || (m.ServerID != "" && m.ServerID != agent) || len(m.Hostname) > 256 || !validMeasurement(m.CPUPct, m.MemPct, m.DiskFreeGB, m.MemTotalGB, m.DiskTotalGB, m.DiskIOPS, m.NetMBps) || m.CPUPct > 100 || m.MemPct > 100 || m.CPUCores < 0 || m.CPUCores > 1_000_000 || m.ConcurrentUsers < 0 || m.ConcurrentUsers > 1_000_000_000 {
				return errors.New("ingest: invalid metric identity or values")
			}
			for _, counter := range []uint64{m.NetSentBytes, m.NetRecvBytes, m.DiskReadOps, m.DiskWriteOps, m.NetSentExternal, m.NetRecvExternal, m.NetSentInternal, m.NetRecvInternal} {
				if counter > math.MaxInt64 {
					return errors.New("ingest: counter exceeds signed storage range")
				}
			}
			m.TenantID, m.ServerID = tenant, agent
			m.Timestamp = m.Timestamp.UTC()
			timestamp = m.Timestamp
		} else {
			p := e.Process
			if p.ID != 0 || (p.TenantID != "" && p.TenantID != tenant) || (p.ServerID != "" && p.ServerID != agent) || len(p.Hostname) > 256 || p.Name == "" || len(p.Name) > 512 || p.PID < 0 || !validMeasurement(p.CPUPct, p.MemMB) {
				return errors.New("ingest: invalid process identity or values")
			}
			p.TenantID, p.ServerID = tenant, agent
			p.Timestamp = p.Timestamp.UTC()
			timestamp = p.Timestamp
		}
		if timestamp.IsZero() || timestamp.After(now.Add(time.Minute)) || timestamp.Before(now.Add(-MaxEventAge)) {
			return fmt.Errorf("ingest: timestamp outside the supported 30-day delivery window")
		}
	}
	return nil
}
