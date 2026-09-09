package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"Zeus/agent"
	"Zeus/storage"
	"github.com/jackc/pgx/v5"
	"github.com/kardianos/service"
)

func envOr(name string, target *string, names ...string) {
	if explicitFlags[name] {
		return
	}
	for _, key := range names {
		if value := os.Getenv(key); value != "" {
			*target = value
			return
		}
	}
}

// Precedence: explicit flags, process environment, one protected config file,
// defaults. No working-directory/executable-directory search or silent fallback.
func loadConfigEnv() error {
	path := *flagConfig
	if path == "" {
		path = os.Getenv("WMONITOR_CONFIG")
	}
	explicit := path != ""
	if path == "" {
		path = machineConfigEnvPath()
	}
	if !filepath.IsAbs(path) {
		return errors.New("config path must be absolute")
	}
	body, err := readProtectedFile(path, 64<<10)
	if !explicit && os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return errors.New("protected configuration file could not be read safely")
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(body)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || (!strings.HasPrefix(key, "WMONITOR_") && key != "PORT") || strings.ContainsAny(key, " \t\r\n") {
			return errors.New("invalid config key or line")
		}
		if _, repeated := values[key]; repeated {
			return errors.New("duplicate config key")
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if scanner.Err() != nil {
		return errors.New("config line exceeds limit")
	}
	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return errors.New("config environment initialization failed")
			}
		}
	}
	return nil
}

func readProtectedFile(path string, limit int64) ([]byte, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("protected file path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("protected path must be a regular file")
	}
	if err := checkConfigPermissions(path, info); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("protected file changed during open")
	}
	body, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(body)) > limit {
		return nil, errors.New("protected file read failed or exceeded size limit")
	}
	return body, nil
}

func machineConfigEnvPath() string {
	if runtime.GOOS == "windows" {
		root := os.Getenv("PROGRAMDATA")
		if root == "" {
			root = `C:\ProgramData`
		}
		return filepath.Join(root, "wmonitor", "config.env")
	}
	return "/etc/wmonitor/config.env"
}

func applyEnvConfig() {
	envOr("agent", flagAgentHub, "WMONITOR_AGENT_HUB")
	envOr("enroll-code", flagEnrollCode, "WMONITOR_ENROLL_CODE")
	envOr("db", flagDB, "WMONITOR_DB")
	envOr("port", flagPort, "WMONITOR_PORT", "PORT")
	envOr("external-iface", flagExternalIface, "WMONITOR_EXTERNAL_IFACE")
	envOr("app-port", flagAppPort, "WMONITOR_APP_PORT", "WMONITOR_APP_PORTS")
	if !explicitFlags["hub"] {
		*flagHub = os.Getenv("WMONITOR_MODE") == "hub" || os.Getenv("WMONITOR_HUB") == "true"
	}
	if (!service.Interactive() || *flagInstall) && os.Getenv(storage.EnvDataDir) == "" {
		path := "/var/lib/wmonitor"
		if runtime.GOOS == "windows" {
			path = filepath.Join(filepath.Dir(machineConfigEnvPath()), "data")
		}
		os.Setenv(storage.EnvDataDir, path)
	}
}

func validateConfig() error {
	mode := os.Getenv("WMONITOR_MODE")
	if mode != "" && mode != "hub" && mode != "agent" && mode != "standalone" {
		return errors.New("WMONITOR_MODE must be hub, agent or standalone")
	}
	if raw := os.Getenv("WMONITOR_HUB"); raw != "" && raw != "true" && raw != "false" {
		return errors.New("WMONITOR_HUB must be true or false")
	}
	if *flagHub && *flagAgentHub != "" {
		return errors.New("Hub and agent modes are mutually exclusive")
	}
	if mode == "agent" && *flagAgentHub == "" {
		return errors.New("agent mode requires a Hub destination")
	}
	if *flagDB != "sqlite" && *flagDB != "postgres" {
		return errors.New("database must be sqlite or postgres")
	}
	port, err := strconv.Atoi(*flagPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("invalid HTTP port")
	}
	if *flagAppPort != "" && len(parseAppPorts(*flagAppPort)) != len(strings.Split(*flagAppPort, ",")) {
		return errors.New("invalid application port list")
	}
	if *flagRunFor < 0 || *flagSince <= 0 || *flagSince > 365*24*time.Hour || *flagUserWindow <= 0 || *flagUserWindow > 24*time.Hour {
		return errors.New("invalid duration range")
	}
	if *flagTTL <= 0 || *flagMaxUses < 1 {
		return errors.New("invalid enrollment limits")
	}
	if *flagExportFlt != "" && *flagExportFlt != "daily" && *flagExportFlt != "weekly" && *flagExportFlt != "monthly" {
		return errors.New("invalid export filter")
	}
	if *flagAgentHub != "" {
		if err := agent.RequireHTTPSHub(*flagAgentHub); err != nil {
			return err
		}
		u, err := url.Parse(*flagAgentHub)
		if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("Hub must be an origin without credentials, path, query or fragment")
		}
	}
	if defaultAPIKey != "" || defaultEnrollCode != "" {
		return errors.New("embedded credentials are not supported; provision protected config")
	}
	for _, key := range []string{"WMONITOR_DISK_BUDGET_BYTES", "WMONITOR_DAILY_ROW_QUOTA", "WMONITOR_DAILY_BYTE_QUOTA", "WMONITOR_AGENT_DAILY_ROW_QUOTA", "WMONITOR_AGENT_DAILY_BYTE_QUOTA"} {
		if raw := os.Getenv(key); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value <= 0 {
				return errors.New("invalid positive resource budget")
			}
		}
	}
	if *flagDB == "postgres" && *flagAgentHub == "" {
		if _, err := resolveDSN(); err != nil {
			return err
		}
	}
	return nil
}

func positiveEnvInt(key string) (int64, bool) {
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	return value, err == nil && value > 0
}
func resolveMode() string {
	if *flagAgentHub != "" {
		return "agent"
	}
	if *flagHub {
		return "hub"
	}
	return "standalone"
}
func resolveAPIKey() string {
	if *flagAPIKey != "" {
		return *flagAPIKey
	}
	return os.Getenv("WMONITOR_API_KEY")
}
func resolveEnrollCode() string {
	if *flagEnrollCode != "" {
		return *flagEnrollCode
	}
	return os.Getenv("WMONITOR_ENROLL_CODE")
}
func mask(value string) string {
	if value == "" {
		return "(not set)"
	}
	return "(set)"
}
func orNone(value string) string {
	if value == "" {
		return "(not set)"
	}
	return value
}
func maskDSN() string { dsn, _ := resolveDSN(); return mask(dsn) }
func printConfig() {
	fmt.Printf("version: %s (%s)\nmode: %s\nport: %s\ndatabase: %s\nagent destination: %s\napi key: %s\nenrollment code: %s\ndsn: %s\nalert webhook: %s\n", buildVersion, buildCommit, resolveMode(), *flagPort, *flagDB, orNone(*flagAgentHub), mask(resolveAPIKey()), mask(resolveEnrollCode()), maskDSN(), mask(*flagAlertWebhook))
}

func openStore() (storage.Store, *storage.DB, error) {
	switch *flagDB {
	case "postgres":
		dsn, err := resolveDSN()
		if err != nil {
			return nil, nil, err
		}
		pg, err := storage.OpenPostgres(dsn)
		if err != nil {
			return nil, nil, errors.New("PostgreSQL startup failed")
		}
		return pg, nil, nil
	case "sqlite":
		dir, err := storage.DataDir()
		if err != nil {
			return nil, nil, err
		}
		db, err := storage.Open(filepath.Join(dir, "wmonitor.db"))
		return db, db, err
	default:
		return nil, nil, errors.New("unsupported database backend")
	}
}

func resolveDSN() (string, error) {
	var dsn string
	switch {
	case explicitFlags["dsn"] || *flagDSN != "":
		dsn = *flagDSN
	case *flagDSNFile != "":
		body, err := readProtectedFile(*flagDSNFile, 64<<10)
		if err != nil {
			return "", errors.New("protected DSN file unavailable")
		}
		dsn = strings.TrimSpace(string(body))
	default:
		dsn = os.Getenv("WMONITOR_DB_DSN")
	}
	if dsn == "" {
		return "", errors.New("PostgreSQL requires an explicit DSN")
	}
	if err := rejectInsecureRemotePostgres(dsn); err != nil {
		return "", err
	}
	return dsn, nil
}

// pgx resolves URL, keyword and environment inputs. Inspect its actual TLS
// configuration, including fallback destinations, instead of guessing from a
// substring such as host=127.attacker.example or sslmode=require.
func rejectInsecureRemotePostgres(dsn string) error {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return errors.New("invalid PostgreSQL DSN")
	}
	if !isLoopbackDSNHost(config.Host) && !strings.HasPrefix(config.Host, "/") {
		if config.TLSConfig == nil || config.TLSConfig.InsecureSkipVerify || config.TLSConfig.ServerName == "" {
			return errors.New("remote PostgreSQL requires verified TLS (sslmode=verify-full)")
		}
	}
	for _, fallback := range config.Fallbacks {
		if !isLoopbackDSNHost(fallback.Host) && !strings.HasPrefix(fallback.Host, "/") && (fallback.TLSConfig == nil || fallback.TLSConfig.InsecureSkipVerify || fallback.TLSConfig.ServerName == "") {
			return errors.New("insecure PostgreSQL fallback rejected")
		}
	}
	return nil
}

func isLoopbackDSNHost(host string) bool {
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return host == "localhost" || (ip != nil && ip.IsLoopback())
}
func postgresDSNHost(dsn string) string {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return ""
	}
	return config.Host
}
func postgresDSNSSLMode(dsn string) string {
	u, _ := url.Parse(dsn)
	if u != nil && u.Query().Get("sslmode") != "" {
		return u.Query().Get("sslmode")
	}
	return dsnKV(dsn, "sslmode")
}
func dsnKV(dsn, key string) string {
	for _, part := range strings.Fields(dsn) {
		k, v, ok := strings.Cut(part, "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}
func logSafeDSN(string) { log.Print("[storage] PostgreSQL configured; connection details redacted") }

func parseAppPorts(raw string) []uint32 {
	var ports []uint32
	if raw == "" {
		return ports
	}
	for _, value := range strings.Split(raw, ",") {
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err == nil && port > 0 && port <= 65535 {
			ports = append(ports, uint32(port))
		}
	}
	return ports
}
func exportTenant() string {
	if *flagTenant != "" {
		return *flagTenant
	}
	return storage.LocalTenantID
}

func resolveServerID() string {
	dir, err := storage.DataDir()
	if err != nil {
		log.Fatal("stable identity directory unavailable")
	}
	path := filepath.Join(dir, "agent_id")
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			log.Fatal("agent identity path is not a regular file")
		}
		body, err := os.ReadFile(path)
		id := strings.TrimSpace(string(body))
		if err != nil || !validServerID.MatchString(id) {
			log.Fatal("stored agent identity is invalid")
		}
		if explicitServerID != "" && explicitServerID != id {
			log.Fatal("identity change requires reviewed re-enrollment; existing identity was not overwritten")
		}
		return id
	} else if !os.IsNotExist(err) {
		log.Fatal("agent identity unavailable")
	}
	id := explicitServerID
	if id == "" {
		generated, err := storage.NewTenantID()
		if err != nil {
			log.Fatal("identity generation failed")
		}
		id = "a_" + strings.TrimPrefix(generated, "t_")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		log.Fatal("agent identity creation failed; no hostname fallback")
	}
	if _, err := io.WriteString(file, id+"\n"); err != nil {
		file.Close()
		log.Fatal("agent identity write failed")
	}
	if err := file.Sync(); err != nil {
		file.Close()
		log.Fatal("agent identity sync failed")
	}
	if err := file.Close(); err != nil {
		log.Fatal("agent identity close failed")
	}
	return id
}

var secretServiceFlags = map[string]struct{}{"-api-key": {}, "--api-key": {}, "-dsn": {}, "--dsn": {}, "-enroll-code": {}, "--enroll-code": {}, "-alert-slack": {}, "--alert-slack": {}, "-alert-webhook": {}, "--alert-webhook": {}}

func serviceSafeArgs(args []string) []string {
	var out []string
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			continue
		}
		name, _, equal := strings.Cut(arg, "=")
		if name == "-install" || name == "--install" {
			continue
		}
		if _, secret := secretServiceFlags[name]; secret {
			skip = !equal
			continue
		}
		out = append(out, arg)
	}
	return out
}
