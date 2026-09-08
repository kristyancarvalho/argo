package unit_test

import "testing"

func TestRequiredFailurePropagates(t *testing.T) {
	t.Fatal("intentional unmerged workflow failure proof")
}
