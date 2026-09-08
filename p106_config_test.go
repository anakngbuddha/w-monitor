package main

import (
	"strings"
	"testing"
)

func TestMaskDoesNotLeakCanarySecret(t *testing.T) {
	const canary = "wma_CANARY_SECRET_xyz"
	got := mask(canary)
	if strings.Contains(got, "CANARY") || strings.Contains(got, "wma_") || strings.Contains(got, canary) {
		t.Fatalf("mask leaked secret material: %q", got)
	}
	if mask("") != "(not set)" {
		t.Fatalf("empty mask = %q", mask(""))
	}
	if mask("x") != "(set)" {
		t.Fatalf("set mask = %q, want (set)", mask("x"))
	}
}

func TestServiceSafeArgsStripsSecrets(t *testing.T) {
	in := []string{"-install", "-agent", "https://hub.example", "-api-key", "wma_CANARY_SECRET", "-port", "8080", "-dsn", "postgres://u:p@db/x"}
	out := serviceSafeArgs(in)
	joined := strings.Join(out, " ")
	if strings.Contains(joined, "wma_CANARY_SECRET") || strings.Contains(joined, "postgres://") || strings.Contains(joined, "-api-key") || strings.Contains(joined, "-dsn") || strings.Contains(joined, "-install") {
		t.Fatalf("service args still contain secrets or install flag: %v", out)
	}
	if len(out) < 4 || out[0] != "-agent" || out[1] != "https://hub.example" {
		t.Fatalf("expected hub URL preserved, got %v", out)
	}
}

func TestRejectInsecureRemotePostgres(t *testing.T) {
	if err := rejectInsecureRemotePostgres("postgres://u:p@db.example.com/x?sslmode=disable"); err == nil {
		t.Fatal("remote disable must fail")
	}
	if err := rejectInsecureRemotePostgres("postgres://u:p@db.example.com/x?sslmode=require"); err != nil {
		t.Fatal(err)
	}
	if err := rejectInsecureRemotePostgres("postgres://u:p@127.0.0.1/x?sslmode=disable"); err != nil {
		t.Fatalf("loopback may omit TLS: %v", err)
	}
	if err := rejectInsecureRemotePostgres("host=db.example.com user=u password=p sslmode=disable"); err == nil {
		t.Fatal("libpq remote disable must fail")
	}
	if err := rejectInsecureRemotePostgres("host=db.example.com sslmode=verify-full"); err != nil {
		t.Fatal(err)
	}
}
