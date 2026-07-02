// Package multiplexer abstracts a terminal multiplexer backend (herdr or tmux)
// behind a single interface the Watcher uses to inject Requests, capture output,
// and detect when an Agent instance is Idle.
package multiplexer

// Pane describes a single multiplexer pane discovered on the system.
type Pane struct {
	ID      string
	CWD     string
	Command string
}

// Multiplexer is the backend contract used by the Watcher. Real herdr/tmux
// implementations arrive in Task 8; Task 1 drives it with a mock.
type Multiplexer interface {
	// Inject types text into the pane (used only for Requests, only while Idle).
	Inject(paneID, text string) error
	// Capture returns the pane's current output.
	Capture(paneID string) (string, error)
	// IsIdle reports whether the pane's agent is not mid-task.
	IsIdle(paneID string) (bool, error)
	// ListPanes enumerates the panes known to the backend.
	ListPanes() ([]Pane, error)
}
