package calc

import "testing"

func TestAverage(t *testing.T) {
	if got := Average([]int{1, 2}); got != 1.5 {
		t.Fatalf("Average(1, 2) = %v, want 1.5", got)
	}

	if got := Average(nil); got != 0 {
		t.Fatalf("Average() = %v, want 0", got)
	}
}
