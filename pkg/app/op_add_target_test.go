package app

import "testing"

func TestAddTargetOpRegistered(t *testing.T) {
	region := ""
	r := Resource(&region, nil)
	op, ok := r.Operations["add-target"]
	if !ok {
		t.Fatal("add-target operation not registered")
	}
	if op.Name != "add-target" {
		t.Errorf("Name = %q; want add-target", op.Name)
	}
	// Verify required fields exist.
	flags := map[string]bool{}
	for _, f := range op.Fields {
		flags[f.Flag] = true
	}
	for _, want := range []string{"name", "env", "instance"} {
		if !flags[want] {
			t.Errorf("missing field %q", want)
		}
	}
}
