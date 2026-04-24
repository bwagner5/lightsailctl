package names

import (
	"strings"
	"testing"
)

func TestRandomShape(t *testing.T) {
	for i := 0; i < 50; i++ {
		n := Random()
		if !strings.Contains(n, "-") || len(n) < 3 {
			t.Fatalf("unexpected name: %q", n)
		}
	}
}
