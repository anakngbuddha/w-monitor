package agent

import (
	"fmt"
	"net/http"
	"path/filepath"
)

// NewWithDataDir constructs an agent with an explicit spool root. Unlike New,
// it never discovers a production data directory or silently disables spooling
// on a filesystem error. Tests must provide t.TempDir().
//
// This constructor preserves the current delivery behavior. It does not claim
// to resolve the separate transport, origin-binding, or spool-race findings.
func NewWithDataDir(hubURL, apiKey, dataDir string) (*Agent, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) {
		return nil, fmt.Errorf("agent: data directory must be absolute")
	}
	sp, err := NewSpool(filepath.Clean(dataDir))
	if err != nil {
		return nil, err
	}
	return &Agent{
		hubURL: hubURL,
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: postTimeout,
		},
		spool:     sp,
		drainWake: make(chan struct{}, 1),
	}, nil
}
