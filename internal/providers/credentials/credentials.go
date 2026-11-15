// Package credentials keeps what the built-in providers need to reach a
// server: an API key a person gave to a provider's login, or what signing in
// with OAuth gave it. It is the providers' own: each keeps a file that only
// the user can read, and the host never sees it.
package credentials

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
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
	APIKey string `json:"api_key,omitempty"`
	// ClientID and HostID are this installation as the server registered
	// it, which signing out keeps, so signing in again is not a new one.
	ClientID string `json:"client_id,omitempty"`
	HostID   string `json:"host_id,omitempty"`
	Grant    *Grant `json:"grant,omitempty"`
}

// Grant is what signing in to a server with OAuth gave a provider: tokens
// for one account, which it renews as they expire, and who that account is.
type Grant struct {
	Subject      string    `json:"subject"`
	Email        string    `json:"email,omitempty"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresAt    time.Time `json:"expires_at"`
	// RefreshAfter is the earliest the server takes a renewal; zero is any
	// time.
	RefreshAfter time.Time `json:"refresh_after,omitzero"`
	Scopes       []string  `json:"scopes"`
	// IDToken is the last proof of who the account is, which names it when
	// signing in again.
	IDToken string `json:"id_token,omitempty"`
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

	kept := servers[address]
	kept.APIKey = key
	servers[address] = kept

	return s.write(servers)
}

// Remove forgets the API key stored for server, and reports whether there
// was one.
func (s Store) Remove(address string) (bool, error) {
	servers, err := s.read()

	if err != nil {
		return false, err
	}

	kept, found := servers[address]

	if !found || kept.APIKey == "" {
		return false, nil
	}

	kept.APIKey = ""
	servers[address] = kept

	return true, s.write(servers)
}

// Registration returns this installation as server registered it: the
// client it issued, and the host this installation is to it. Either is
// empty while it is not known.
func (s Store) Registration(address string) (clientID, hostID string, err error) {
	servers, err := s.read()

	if err != nil {
		return "", "", err
	}

	return servers[address].ClientID, servers[address].HostID, nil
}

// SetRegistration keeps the client server issued and the host this
// installation is to it.
func (s Store) SetRegistration(address, clientID, hostID string) error {
	servers, err := s.read()

	if err != nil {
		return err
	}

	kept := servers[address]
	kept.ClientID, kept.HostID = clientID, hostID
	servers[address] = kept

	return s.write(servers)
}

// Grant returns what signing in to server gave, or nil when nobody is
// signed in.
func (s Store) Grant(address string) (*Grant, error) {
	servers, err := s.read()

	if err != nil {
		return nil, err
	}

	return servers[address].Grant, nil
}

// SetGrant keeps what signing in to server gave, and nil signs out, keeping
// the registration.
func (s Store) SetGrant(address string, grant *Grant) error {
	servers, err := s.read()

	if err != nil {
		return err
	}

	kept := servers[address]
	kept.Grant = grant
	servers[address] = kept

	return s.write(servers)
}

// Locked runs do while no other process holds the store, so two processes
// of a provider do not renew one grant with the same token at once.
func (s Store) Locked(do func() error) error {
	if s.Path == "" {
		return do()
	}

	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		return err
	}

	lock, err := os.OpenFile(s.Path+".lock", os.O_CREATE|os.O_RDWR, 0o600)

	if err != nil {
		return err
	}

	defer lock.Close()

	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}

	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	return do()
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
