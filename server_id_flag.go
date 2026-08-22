package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"Zeus/storage"
)

// persistentServerIDFlag stores an explicit agent identity in the same file
// used by the generated identity. Keeping the override persistent avoids a
// split series if an operator accidentally omits the flag on a later restart.
type persistentServerIDFlag struct{}

func (persistentServerIDFlag) String() string { return "" }

func (persistentServerIDFlag) Set(raw string) error {
	id := strings.TrimSpace(raw)
	if id == "" {
		return fmt.Errorf("server-id cannot be empty")
	}
	if strings.ContainsAny(id, "\r\n\t") {
		return fmt.Errorf("server-id cannot contain control whitespace")
	}
	if len(id) > 128 {
		return fmt.Errorf("server-id cannot exceed 128 characters")
	}

	dir, err := storage.DataDir()
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	path := filepath.Join(dir, "agent_id")
	// Write directly rather than rename over the existing file: Windows does not
	// allow os.Rename to replace a destination, which made overrides fail on the
	// platform that needs this option most often.
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return fmt.Errorf("write server-id: %w", err)
	}
	return nil
}

func init() {
	flag.Var(persistentServerIDFlag{}, "server-id", "Stable agent server ID override (persisted in the data directory)")
}
