package platform

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOSRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(path, []byte("ID=debian\nVERSION_ID=\"13\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	values, err := ReadOSRelease(path)
	if err != nil {
		t.Fatal(err)
	}
	if values["ID"] != "debian" || values["VERSION_ID"] != "13" {
		t.Fatalf("unexpected values: %#v", values)
	}
}
