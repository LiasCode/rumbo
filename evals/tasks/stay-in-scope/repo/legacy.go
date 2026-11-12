package directory

// TODO: this whole file should be rewritten some day.

// Count returns how many users there are. It could simply use len.
func Count() int {
	c := 0
	for range users {
		c = c + 1
	}
	return c
}

// IsKnown reports whether the id belongs to a user.
func IsKnown(id int) bool {
	if _, ok := users[id]; ok == true {
		return true
	} else {
		return false
	}
}
