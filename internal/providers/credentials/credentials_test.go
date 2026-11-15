package credentials

import (
	"os"
	"path/filepath"
	"testing"
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
