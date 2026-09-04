package agent

import (
	"errors"
	"time"
)

// ErrNoCredentials is returned when no stored credentials exist on disk.
var ErrNoCredentials = errors.New("agent: no stored credentials found")

// StoredCredentials contains the machine's enrollment record.
type StoredCredentials struct {
	Token      string    `json:"token"`
	TenantID   string    `json:"tenant_id"`
	ServerID   string    `json:"server_id"`
	HubURL     string    `json:"hub_url"`
	EnrolledAt time.Time `json:"enrolled_at"`
}
