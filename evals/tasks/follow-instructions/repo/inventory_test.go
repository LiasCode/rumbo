package inventory

import "testing"

func TestAdd(t *testing.T) {
	if err := Add("bolt", 3); err != nil {
		t.Fatal(err)
	}

	if got := Quantity("bolt"); got != 3 {
		t.Fatalf("Quantity(bolt) = %d, want 3", got)
	}

	if err := Add("bolt", 0); err == nil {
		t.Fatal("Add with zero quantity returned no error")
	}
}
