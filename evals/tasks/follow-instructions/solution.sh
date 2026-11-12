set -e
cat >> inventory.go <<'GO'

// Remove deletes the named item from stock.
func Remove(name string) error {
	if _, found := stock[name]; !found {
		return fmt.Errorf("inventory: no item named %q", name)
	}

	delete(stock, name)

	return nil
}
GO
cat >> inventory_test.go <<'GO'

func TestRemove(t *testing.T) {
	if err := Add("washer", 1); err != nil {
		t.Fatal(err)
	}

	if err := Remove("washer"); err != nil {
		t.Fatal(err)
	}

	if err := Remove("washer"); err == nil {
		t.Fatal("Remove of a missing item returned no error")
	}
}
GO
