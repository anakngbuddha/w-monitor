package agent_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"

	"Zeus/agent"
)

func fixtureCredentials() agent.StoredCredentials {
	return agent.StoredCredentials{
		Token:      "wma_test_token_12345",
		TenantID:   "t_test_tenant",
		ServerID:   "srv-test-01",
		HubURL:     "https://hub.example.com",
		EnrolledAt: time.Now().UTC().Truncate(time.Second),
	}
}

func newCredentialFixture(t *testing.T, dir string) *agent.CredentialStore {
	t.Helper()
	store, err := agent.NewCredentialStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestCredentialsSaveLoadClear(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := newCredentialFixture(t, dir)
	creds := fixtureCredentials()
	if _, err := store.Load(); !errors.Is(err, agent.ErrNoCredentials) {
		t.Fatalf("load before save: %v", err)
	}
	if err := store.Save(creds); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := newCredentialFixture(t, dir).Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(*loaded, creds) {
		t.Fatal("credential round trip changed fields")
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("repeated clear: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, agent.ErrNoCredentials) {
		t.Fatalf("load after clear: %v", err)
	}
}

func TestCredentialFixtureDoesNotChangeOutsideCanary(t *testing.T) {
	t.Parallel()
	// The canary is disposable and outside the credential fixture directory.
	root := t.TempDir()
	canary := filepath.Join(root, "outside-canary")
	want := []byte("unchanged-disposable-canary\n")
	if err := os.WriteFile(canary, want, 0600); err != nil {
		t.Fatal(err)
	}
	store := newCredentialFixture(t, filepath.Join(root, "fixture"))
	if err := store.Save(fixtureCredentials()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(canary)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("credential fixture changed its outside canary")
	}
}

func TestCredentialStoresAreIndependent(t *testing.T) {
	t.Parallel()
	a := newCredentialFixture(t, t.TempDir())
	b := newCredentialFixture(t, t.TempDir())
	wantA := fixtureCredentials()
	wantB := fixtureCredentials()
	wantB.ServerID = "srv-test-02"
	if err := a.Save(wantA); err != nil {
		t.Fatal(err)
	}
	if err := b.Save(wantB); err != nil {
		t.Fatal(err)
	}
	if err := a.Clear(); err != nil {
		t.Fatal(err)
	}
	got, err := b.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*got, wantB) {
		t.Fatal("one store modified another store")
	}
}

func TestCredentialStoreRejectsImplicitPaths(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"", ".", "relative"} {
		if _, err := agent.NewCredentialStore(dir); err == nil {
			t.Errorf("accepted implicit directory %q", dir)
		}
	}
	for _, store := range []*agent.CredentialStore{nil, new(agent.CredentialStore)} {
		if _, err := store.Load(); err == nil {
			t.Error("invalid store loaded a default location")
		}
		if err := store.Save(fixtureCredentials()); err == nil {
			t.Error("invalid store wrote a default location")
		}
		if err := store.Clear(); err == nil {
			t.Error("invalid store cleared a default location")
		}
	}
}

func TestCredentialStoreConstructionHasNoFilesystemSideEffects(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "not-created")
	store := newCredentialFixture(t, dir)
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("constructor created directory: %v", err)
	}
	if _, err := store.Load(); !errors.Is(err, agent.ErrNoCredentials) {
		t.Fatalf("missing store load: %v", err)
	}
	if err := store.Clear(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("read/clear created directory: %v", err)
	}
}

func TestCredentialFixtureUsesPlatformEncoding(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := newCredentialFixture(t, dir)
	creds := fixtureCredentials()
	if err := store.Save(creds); err != nil {
		t.Fatal(err)
	}
	name := "token.json"
	if runtime.GOOS == "windows" {
		name = "token.dat"
	}
	path := filepath.Join(dir, name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if bytes.Contains(data, []byte(creds.Token)) {
			t.Fatal("Windows credential file contains plaintext token")
		}
	} else {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("credential permissions = %o", info.Mode().Perm())
		}
	}
}
