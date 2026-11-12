set -e
cat > calc.go <<'GO'
// Package calc has small numeric helpers.
package calc

// Average returns the mean of values, or zero when there are none.
func Average(values []int) float64 {
	if len(values) == 0 {
		return 0
	}

	sum := 0

	for _, value := range values {
		sum += value
	}

	return float64(sum) / float64(len(values))
}
GO
