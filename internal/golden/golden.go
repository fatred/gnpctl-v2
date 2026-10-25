// Package golden is test support: it loads the Python-generated golden files in
// testdata/golden. It has no production caller; a non-test file is needed so
// the package can be imported by tests.
package golden

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Body returns the output body of a golden CLI capture, dropping its leading
// exit-status line.
func Body(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", name))
	if err != nil {
		t.Fatal(err)
	}
	i := strings.IndexByte(string(data), '\n')
	if i < 0 {
		t.Fatalf("%s: missing exit line", name)
	}
	return string(data)[i+1:]
}

// CopyDB copies the Python-written state fixture into a temp dir so tests
// cannot modify the checked-in file.
func CopyDB(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "python_state.db"))
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "state.db")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return dst
}
