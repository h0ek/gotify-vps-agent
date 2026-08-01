package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestVersionReportsBuildAndGoToolchain(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	code := New(strings.NewReader(""), &output, &errors).Run(context.Background(), []string{"version"})
	if code != 0 {
		t.Fatalf("version returned %d: %s", code, errors.String())
	}
	for _, expected := range []string{"gotify-vps-agent ", "commit=", "built=", "go=go"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("version output %q does not contain %q", output.String(), expected)
		}
	}
}

func TestUnknownCommandFails(t *testing.T) {
	var output bytes.Buffer
	var errors bytes.Buffer
	code := New(strings.NewReader(""), &output, &errors).Run(context.Background(), []string{"unknown"})
	if code != 2 || !strings.Contains(errors.String(), "Unknown command") {
		t.Fatalf("unexpected result code=%d stderr=%q", code, errors.String())
	}
}
