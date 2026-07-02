package multiplexer

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// fakeHerdrPath returns a temp dir containing an executable named herdr, so
// Detect's PATH probe succeeds without any real herdr installed.
func fakeHerdrPath(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake-PATH probe is unix-only")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake herdr: %v", err)
	}
	return dir
}

func TestDetectPrecedence(t *testing.T) {
	cases := []struct {
		name      string
		herdrEnv  string
		fakeHerdr bool
		wantHerdr bool
	}{
		{"HERDR_ENV=1 selects herdr even without herdr on PATH", "1", false, true},
		{"herdr on PATH selects herdr without HERDR_ENV", "", true, true},
		{"neither falls back to tmux", "", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HERDR_ENV", tc.herdrEnv)
			if tc.fakeHerdr {
				t.Setenv("PATH", fakeHerdrPath(t))
			} else {
				t.Setenv("PATH", t.TempDir()) // empty dir: no herdr, no real backends
			}
			mux := Detect()
			if _, isHerdr := mux.(*Herdr); isHerdr != tc.wantHerdr {
				t.Fatalf("Detect() = %T, want herdr=%v", mux, tc.wantHerdr)
			}
		})
	}
}
