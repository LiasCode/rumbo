package credentials

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestKeysAreKeptByServerAndOnlyForTheUser(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "rumbo", "credentials", "openai.json")}

	if key, err := store.APIKey("https://api.openai.com/v1"); err != nil || key != "" {
		t.Fatalf("before anything is stored: %q, %v", key, err)
	}

	store.SetAPIKey("https://api.openai.com/v1", "sk-one")
	store.SetAPIKey("http://localhost:11434/v1", "sk-two")

	if key, _ := store.APIKey("https://api.openai.com/v1"); key != "sk-one" {
		t.Fatalf("the first server's key = %q", key)
	}

	for path, want := range map[string]os.FileMode{store.Path: 0o600, filepath.Dir(store.Path): 0o700} {
		info, err := os.Stat(path)

		if err != nil {
			t.Fatal(err)
		}

		if info.Mode().Perm() != want {
			t.Fatalf("%s: mode %v; want %v", path, info.Mode().Perm(), want)
		}
	}

	// Removing one server's key leaves the other's.
	if removed, err := store.Remove("https://api.openai.com/v1"); !removed || err != nil {
		t.Fatalf("remove = %v, %v", removed, err)
	}

	if removed, _ := store.Remove("https://api.openai.com/v1"); removed {
		t.Fatal("a key was removed twice")
	}

	if key, _ := store.APIKey("http://localhost:11434/v1"); key != "sk-two" {
		t.Fatalf("the other server's key = %q", key)
	}

	if leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(store.Path), ".credentials-*")); len(leftovers) > 0 {
		t.Fatalf("temporary files were left: %v", leftovers)
	}
}

func TestAStoreWithNowhereToKeepCredentials(t *testing.T) {
	var store Store

	if key, err := store.APIKey("https://api.anthropic.com"); key != "" || err != nil {
		t.Fatalf("an empty store found %q, %v", key, err)
	}

	if err := store.SetAPIKey("https://api.anthropic.com", "sk"); err == nil {
		t.Fatal("an empty store kept a key")
	}
}

func TestABrokenFileIsReported(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "anthropic.json")}
	os.WriteFile(store.Path, []byte("not json"), 0o600)

	if _, err := store.APIKey("https://api.anthropic.com"); err == nil {
		t.Fatal("a broken file was read as empty")
	}
}

// What signing in gives is kept apart from the key, and signing out keeps
// the registration, so signing in again is not a new one.
func TestAGrantIsKeptWithItsRegistration(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "chatgpt.json")}
	address := "https://api.openai.com/v1"

	if grant, err := store.Grant(address); grant != nil || err != nil {
		t.Fatalf("before signing in: %+v, %v", grant, err)
	}

	store.SetRegistration(address, "oaiapp_1", "urn:uuid:1")
	store.SetGrant(address, &Grant{Subject: "user-1", AccessToken: "a", RefreshToken: "r", ExpiresAt: time.Unix(100, 0).UTC()})

	if grant, _ := store.Grant(address); grant == nil || grant.Subject != "user-1" || !grant.ExpiresAt.Equal(time.Unix(100, 0)) {
		t.Fatalf("grant = %+v", grant)
	}

	store.SetGrant(address, nil)

	if client, host, _ := store.Registration(address); client != "oaiapp_1" || host != "urn:uuid:1" {
		t.Fatalf("after signing out the registration is %q, %q", client, host)
	}

	if grant, _ := store.Grant(address); grant != nil {
		t.Fatalf("after signing out: %+v", grant)
	}

	// A key stored for the same server leaves the rest as it is, and so
	// does removing it.
	store.SetAPIKey(address, "sk")
	store.Remove(address)

	if client, _, _ := store.Registration(address); client != "oaiapp_1" {
		t.Fatalf("the key took the registration with it: %q", client)
	}
}

// One process holds the store at a time.
func TestTheStoreIsHeldByOneAtATime(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "chatgpt.json")}
	held := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})

	go store.Locked(func() error {
		close(held)
		<-release

		return nil
	})

	<-held

	go func() {
		store.Locked(func() error { return nil })
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("the store was held twice at once")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	<-done
}
