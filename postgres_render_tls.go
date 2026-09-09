package main

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"os"
)

// Render commonly supplies a PEM CA certificate separately while its generated
// connection string still uses sslmode=require. pgx treats that mode as
// encrypted-but-unverified, which the production config intentionally rejects.
// Normalize the deployment-time DSN before config validation so the supplied CA
// is actually used and hostname verification remains enabled.
func init() {
	dsn := os.Getenv("WMONITOR_DB_DSN")
	ca := os.Getenv("WMONITOR_DB_CA_CERT")
	if dsn == "" || ca == "" {
		return
	}

	if _, err := parseCA(ca); err != nil {
		return
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme != "postgres" && u.Scheme != "postgresql" || u.Host == "" {
		return
	}

	q := u.Query()
	if mode := q.Get("sslmode"); mode == "" || mode == "require" {
		q.Set("sslmode", "verify-full")
	}
	if q.Get("sslrootcert") == "" {
		path, err := writeRenderCA(ca)
		if err != nil {
			return
		}
		q.Set("sslrootcert", path)
	}
	u.RawQuery = q.Encode()
	_ = os.Setenv("WMONITOR_DB_DSN", u.String())
}

func parseCA(value string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	remaining := []byte(value)
	found := false
	for len(remaining) > 0 {
		block, rest := pem.Decode(remaining)
		if block == nil {
			break
		}
		remaining = rest
		if block.Type == "CERTIFICATE" {
			if !pool.AppendCertsFromPEM(pem.EncodeToMemory(block)) {
				return nil, fmt.Errorf("invalid PostgreSQL CA certificate")
			}
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("PostgreSQL CA certificate is not PEM encoded")
	}
	return pool, nil
}

func writeRenderCA(ca string) (string, error) {
	file, err := os.CreateTemp("", "wmonitor-postgres-ca-*.pem")
	if err != nil {
		return "", err
	}
	path := file.Name()
	cleanup := func(e error) (string, error) {
		_ = file.Close()
		_ = os.Remove(path)
		return "", e
	}
	if err := file.Chmod(0600); err != nil {
		return cleanup(err)
	}
	if _, err := file.WriteString(ca); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}
