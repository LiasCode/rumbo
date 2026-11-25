// Package home says where rumbo keeps everything of its own: what the user
// chose, their instructions and skills, credentials, plugins, and the
// sessions. It is one directory, the same on every operating system.
package home

import (
	"os"
	"path/filepath"
)

// Dir is rumbo's directory: RUMBO_HOME, or .rumbo in the user's home
// directory.
func Dir() (string, error) {
	if dir := os.Getenv("RUMBO_HOME"); dir != "" {
		return dir, nil
	}

	user, err := os.UserHomeDir()

	if err != nil {
		return "", err
	}

	return filepath.Join(user, ".rumbo"), nil
}
