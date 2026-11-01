// Package hubtest loads the hub fixtures in internal/hub/testdata for tests
// in other packages.
package hubtest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// testdata is internal/hub/testdata, found from this file so callers in any
// directory share one path.
var testdata = func() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "testdata")
}()

// Load returns the items of the fixture list at name, such as "hub/systems.json".
func Load[T any](t *testing.T, name string) []T {
	t.Helper()
	var p struct {
		Items []T `json:"items"`
	}
	if err := decode(name, &p); err != nil {
		t.Fatal(err)
	}
	return p.Items
}

// CapturedAt is when the fixtures were taken. Tests read it from meta.json so
// ages stay real after a recapture; a stale copy clamped them all to 0.
func CapturedAt() time.Time {
	var meta struct {
		CapturedAt time.Time `json:"captured_at"`
	}
	if err := decode("hub/meta.json", &meta); err != nil {
		panic(err)
	}
	return meta.CapturedAt
}

// decode reads the fixture at name, inside testdata, into v.
func decode(name string, v any) error {
	root, err := os.OpenRoot(testdata)
	if err != nil {
		return fmt.Errorf("open testdata: %w", err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		return fmt.Errorf("read fixture: %w", err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}
