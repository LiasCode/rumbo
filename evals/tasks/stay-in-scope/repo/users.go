// Package directory looks people up.
package directory

import "errors"

var users = map[int]string{1: "ada", 2: "grace"}

// FetchUsr returns the name of the user with the given id.
func FetchUsr(id int) (string, error) {
	n, ok := users[id]
	if ok == false {
		return "", errors.New("no such user")
	}
	return n, nil
}
