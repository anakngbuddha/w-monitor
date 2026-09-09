package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

// LocalTenantID is the explicit tenant used for standalone (non-hub) operation.
// Empty tenant is never a wildcard for customer reads.
const LocalTenantID = "t_local"

// DefaultQueryLimit caps metric/process reads unless a caller sets Limit.
const DefaultQueryLimit = 100_000

// ErrTenantRequired is returned when a customer-facing query has no tenant scope.
var ErrTenantRequired = errors.New("storage: tenant scope is required")

// MetricQuery is a bounded, tenant-scoped metrics read.
type MetricQuery struct {
	Ctx       context.Context
	Since     time.Time
	Until     time.Time // zero = no upper bound
	TenantID  string
	ServerID  string
	Limit     int
	AfterUnix int64 // exclusive keyset cursor timestamp (with AfterID)
	AfterID   int64 // exclusive keyset cursor row id
}

// AllTenantsQuerier is the privileged read used by health and alert evaluation.
// It is not a customer export surface.
type AllTenantsQuerier interface {
	QueryMetricsAllTenants(ctx context.Context, since time.Time, limit int) ([]MetricRow, error)
}

// RequireTenant rejects empty or whitespace tenant IDs.
func RequireTenant(id string) error {
	if strings.TrimSpace(id) == "" {
		return ErrTenantRequired
	}
	return nil
}

func queryContext(ctx context.Context) context.Context {
	if ctx != nil {
		return ctx
	}
	return context.Background()
}

func queryLimit(limit int) int {
	return boundedLimit(limit)
}

func normalizeInsertTenant(id string) string {
	if strings.TrimSpace(id) == "" {
		return LocalTenantID
	}
	return id
}
