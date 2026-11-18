package httpx

import "testing"

func TestSafe(t *testing.T) {
	for rel, want := range map[string]string{"": "/b", "a/c": "/b/a/c", "../../etc": "/b/etc", "/etc/passwd": "/b/etc/passwd", "a/../..": "/b"} {
		if got := Safe("/b", rel); got != want {
			t.Errorf("Safe(%q) = %q, quiero %q", rel, got, want)
		}
	}
}
