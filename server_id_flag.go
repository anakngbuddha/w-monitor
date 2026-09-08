package main

import (
	"flag"
	"fmt"
	"regexp"
)

var explicitServerID string
var validServerID = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

// Flag parsing must not touch a user-dependent data path before machine config
// is loaded. resolveServerID persists the validated selection at startup.
type persistentServerIDFlag struct{}
func (persistentServerIDFlag) String() string { return explicitServerID }
func (persistentServerIDFlag) Set(raw string) error {
	if !validServerID.MatchString(raw) { return fmt.Errorf("server-id must contain 1-128 ASCII letters, digits, underscores, dots or hyphens") }
	explicitServerID = raw
	return nil
}
func init() { flag.Var(persistentServerIDFlag{}, "server-id", "Stable agent server identity (validated before use)") }
