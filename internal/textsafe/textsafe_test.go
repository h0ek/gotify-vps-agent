package textsafe

import "testing"

func TestSanitizeRemovesControlsAndLimitsUTF8(t *testing.T) {
	got := Sanitize("ok\x1b[31m\x00żółć", 10)
	if got != "ok[31mżó" {
		t.Fatalf("unexpected sanitized value %q", got)
	}
}
