package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"runtime"
	"time"
)

type enrollReq struct {
	EnrollCode string `json:"enroll_code"`
	ServerID   string `json:"server_id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Version    string `json:"version"`
}

type enrollResp struct {
	Token    string `json:"token"`
	TenantID string `json:"tenant_id"`
	ServerID string `json:"server_id"`
	Error    string `json:"error,omitempty"`
}

// Enroll performs the first-run handshake with the hub and persists the returned agent token.
func Enroll(ctx context.Context, hubURL, enrollCode, serverID, hostname, version string) (*StoredCredentials, error) {
	client := &http.Client{Timeout: 15 * time.Second}

	reqBody, err := json.Marshal(enrollReq{
		EnrollCode: enrollCode,
		ServerID:   serverID,
		Hostname:   hostname,
		OS:         fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH),
		Version:    version,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal enroll request: %w", err)
	}

	url := fmt.Sprintf("%s/api/enroll", hubURL)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("create enroll request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enroll request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		var errResp enrollResp
		_ = json.NewDecoder(resp.Body).Decode(&errResp)
		if errResp.Error != "" {
			return nil, fmt.Errorf("enrollment failed (status %d): %s", resp.StatusCode, errResp.Error)
		}
		return nil, fmt.Errorf("enrollment rejected by hub with status %d", resp.StatusCode)
	}

	var data enrollResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode enroll response: %w", err)
	}

	creds := StoredCredentials{
		Token:      data.Token,
		TenantID:   data.TenantID,
		ServerID:   data.ServerID,
		HubURL:     hubURL,
		EnrolledAt: time.Now(),
	}

	if err := SaveCredentials(creds); err != nil {
		return nil, fmt.Errorf("save enrolled credentials: %w", err)
	}

	return &creds, nil
}
