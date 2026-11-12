// Package calc has small numeric helpers.
package calc

// Average returns the mean of values, or zero when there are none.
func Average(values []int) float64 {
	sum := 0

	for _, value := range values {
		sum += value
	}

	return float64(sum / len(values))
}
