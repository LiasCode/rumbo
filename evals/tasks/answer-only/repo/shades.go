// Package shades names numbers after colors.
package shades

// Classify returns the shade of n.
func Classify(n int) string {
	shade := ""

	if n%3 == 0 {
		shade += "teal"
	}

	if n%5 == 0 {
		if shade != "" {
			shade += "-"
		}

		shade += "amber"
	}

	if shade == "" {
		return "slate"
	}

	return shade
}
