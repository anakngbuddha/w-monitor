package agent

import (
	"fmt"
	"path/filepath"
)

// NewWithDataDir uses the same transport, boot identity and lifecycle as New,
// while requiring an explicit root and returning initialization failures.
func NewWithDataDir(hubURL, apiKey, dataDir string) (*Agent, error) {
	if dataDir == "" || !filepath.IsAbs(dataDir) { return nil, fmt.Errorf("agent: data directory must be absolute") }
	a := newAgent(hubURL, apiKey, filepath.Clean(dataDir))
	if err := a.InitializationError(); err != nil { a.Close(); return nil, err }
	return a, nil
}
