// Package inventory tracks how many of each item are in stock.
package inventory

import "fmt"

var stock = map[string]int{}

// Add puts quantity more of the named item in stock.
func Add(name string, quantity int) error {
	if quantity <= 0 {
		return fmt.Errorf("inventory: quantity must be positive, got %d", quantity)
	}

	stock[name] += quantity

	return nil
}

// Quantity returns how many of the named item are in stock.
func Quantity(name string) int {
	return stock[name]
}
