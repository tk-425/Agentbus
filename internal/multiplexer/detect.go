package multiplexer

import (
	"errors"
	"os"
)

// ErrNoMultiplexer is returned by Detect when the current pane runs under neither
// a supported multiplexer. Its message names both backends so it is actionable on
// its own, without the user having to open a log file.
var ErrNoMultiplexer = errors.New("no supported multiplexer detected: run agentbus inside a tmux or herdr session")

// Detect selects the Multiplexer backend from the current pane's runtime
// environment, not from installed binaries. It checks tmux ($TMUX) first, then
// herdr (HERDR_ENV=1); the tmux-first order resolves the degenerate case of a
// tmux pane launched inside herdr, where the inherited HERDR_ENV signal would
// otherwise win. When neither signal is present it returns ErrNoMultiplexer with
// no backend, so callers abort rather than proceed with an unselected backend.
func Detect() (Multiplexer, error) {
	if os.Getenv("TMUX") != "" {
		return NewTmux(), nil
	}
	if os.Getenv("HERDR_ENV") == "1" {
		return NewHerdr(), nil
	}
	return nil, ErrNoMultiplexer
}
