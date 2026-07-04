package multiplexer

import (
	"errors"
	"testing"
)

// TestDetectKeysOnRuntimeEnvironment covers environment-keyed backend selection:
// tmux when $TMUX is set, herdr when HERDR_ENV=1, tmux first when both are set,
// and the no-multiplexer error with no backend when neither is present. Installed
// binaries never influence the result, so there is no PATH probe to exercise.
func TestDetectKeysOnRuntimeEnvironment(t *testing.T) {
	cases := []struct {
		name     string
		tmux     string
		herdrEnv string
		want     string // "tmux", "herdr", or "none"
	}{
		{"TMUX only selects tmux", "/tmp/tmux-1/default,1,0", "", "tmux"},
		{"HERDR_ENV=1 only selects herdr", "", "1", "herdr"},
		{"both present selects tmux", "/tmp/tmux-1/default,1,0", "1", "tmux"},
		{"neither returns no-multiplexer error", "", "", "none"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TMUX", tc.tmux)
			t.Setenv("HERDR_ENV", tc.herdrEnv)

			mux, err := Detect()

			switch tc.want {
			case "tmux":
				if err != nil {
					t.Fatalf("Detect() error = %v, want nil", err)
				}
				if _, ok := mux.(*Tmux); !ok {
					t.Fatalf("Detect() = %T, want *Tmux", mux)
				}
			case "herdr":
				if err != nil {
					t.Fatalf("Detect() error = %v, want nil", err)
				}
				if _, ok := mux.(*Herdr); !ok {
					t.Fatalf("Detect() = %T, want *Herdr", mux)
				}
			case "none":
				if !errors.Is(err, ErrNoMultiplexer) {
					t.Fatalf("Detect() error = %v, want ErrNoMultiplexer", err)
				}
				if mux != nil {
					t.Fatalf("Detect() = %T, want nil backend on error", mux)
				}
			}
		})
	}
}
