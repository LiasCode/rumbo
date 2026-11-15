// Package credentials keeps what the built-in providers need to reach a
// server, such as an API key a person gave to a provider's login. It is the
// providers' own: each keeps a file that only the user can read, and the
// host never sees it.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Store is the file of one provider, which holds the credentials of each
// server it reaches, by the server's address.
type Store struct {
	// Path is the file. Empty means there is nowhere to keep credentials,
	// so none are found and none can be stored.
	Path string
}

// Default returns the store of the named provider, in the user's
// configuration directory. It is empty when that directory is not known.
func Default(provider string) Store {
	dir, err := os.UserConfigDir()

	if err != nil {
		return Store{}
	}

	return Store{Path: filepath.Join(dir, "rumbo", "credentials", provider+".json")}
}

// server is what is kept for one server.
type server struct {
	APIKey string `json:"api_key"`
}

// APIKey returns the key stored for server, or an empty one when none is.
func (s Store) APIKey(address string) (string, error) {
	servers, err := s.read()

	if err != nil {
		return "", err
	}

	return servers[address].APIKey, nil
}

// SetAPIKey stores key for server, and leaves the other servers as they are.
func (s Store) SetAPIKey(address, key string) error {
	servers, err := s.read()

	if err != nil {
		return err
	}

	servers[address] = server{APIKey: key}

	return s.write(servers)
}

// Remove forgets what is stored for server, and reports whether there was
// anything.
func (s Store) Remove(address string) (bool, error) {
	servers, err := s.read()

	if err != nil {
		return false, err
	}

	if _, found := servers[address]; !found {
		return false, nil
	}

	delete(servers, address)

	return true, s.write(servers)
}

func (s Store) read() (map[string]server, error) {
	servers := map[string]server{}

	if s.Path == "" {
		return servers, nil
	}

	raw, err := os.ReadFile(s.Path)

	if errors.Is(err, os.ErrNotExist) {
		return servers, nil
	}

	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(raw, &servers); err != nil {
		return nil, fmt.Errorf("the credentials in %s cannot be read: %w", s.Path, err)
	}

	return servers, nil
}

// write replaces the file whole, readable only by the user, so a reader
// never finds half of it.
func (s Store) write(servers map[string]server) error {
	if s.Path == "" {
		return errors.New("there is no configuration directory to keep credentials in")
	}

	dir := filepath.Dir(s.Path)

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	raw, _ := json.MarshalIndent(servers, "", "  ")

	// A temporary file is created readable only by its owner.
	file, err := os.CreateTemp(dir, ".credentials-*")

	if err != nil {
		return err
	}

	defer os.Remove(file.Name())

	if _, err := file.Write(append(raw, '\n')); err != nil {
		file.Close()
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	return os.Rename(file.Name(), s.Path)
}
