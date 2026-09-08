package main

import (
	"strings"
	"testing"
)

func TestMaskDoesNotLeakCanarySecret(t *testing.T) {
	const canary = "wma_CANARY_SECRET_xyz"
	if got := mask(canary); strings.Contains(got, "CANARY") || strings.Contains(got, "wma_") || got != "(set)" { t.Fatalf("secret masking failed: %q", got) }
	if mask("") != "(not set)" { t.Fatal("empty secret mask changed") }
}

func TestServiceSafeArgsStripsSecrets(t *testing.T) {
	args := []string{"-install", "-agent", "https://hub.example", "-api-key", "wma_CANARY_SECRET", "-port", "8080", "-dsn", "postgres://u:p@db/x", "--alert-webhook=https://hooks.example/secret"}
	out := serviceSafeArgs(args)
	joined := strings.Join(out, " ")
	for _, forbidden := range []string{"wma_CANARY_SECRET", "postgres://", "-api-key", "-dsn", "-install", "hooks.example"} { if strings.Contains(joined, forbidden) { t.Fatalf("service args leaked %q", forbidden) } }
	if len(out) < 4 || out[0] != "-agent" || out[1] != "https://hub.example" { t.Fatalf("destination was lost: %v", out) }
}

func TestRejectInsecureRemotePostgres(t *testing.T) {
	for _, dsn := range []string{"postgres://u:p@db.example/x?sslmode=disable", "postgres://u:p@db.example/x?sslmode=require", "postgres://u:p@db.example/x?sslmode=verify-ca", "host=127.attacker.example sslmode=disable", "host=db.example sslmode=disable"} { if err := rejectInsecureRemotePostgres(dsn); err == nil { t.Error("unverified remote database accepted") } }
	for _, dsn := range []string{"postgres://u:p@127.0.0.1/x?sslmode=disable", "host=db.example sslmode=verify-full"} { if err := rejectInsecureRemotePostgres(dsn); err != nil { t.Fatalf("valid DB policy rejected: %v", err) } }
}
