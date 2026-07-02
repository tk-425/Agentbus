package multiplexer

import (
	"os"
	"os/exec"
)

// Detect picks the Multiplexer backend: herdr when HERDR_ENV=1 or herdr is on
// PATH (the reliable backend per ADR-0001), otherwise tmux (best-effort).
func Detect() Multiplexer {
	if os.Getenv("HERDR_ENV") == "1" {
		return NewHerdr()
	}
	if _, err := exec.LookPath("herdr"); err == nil {
		return NewHerdr()
	}
	return NewTmux()
}
