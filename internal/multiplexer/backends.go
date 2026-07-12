package multiplexer

// Backend pairs a stable backend tag with its Multiplexer implementation.
type Backend struct {
	Name        string
	Multiplexer Multiplexer
}

// Backends returns supported backends in Detect's precedence order.
func Backends() []Backend {
	return []Backend{
		{Name: "tmux", Multiplexer: NewTmux()},
		{Name: "herdr", Multiplexer: NewHerdr()},
	}
}

// For constructs the backend named by a persisted Agent instance tag.
func For(name string) (Multiplexer, bool) {
	switch name {
	case "tmux":
		return NewTmux(), true
	case "herdr":
		return NewHerdr(), true
	default:
		return nil, false
	}
}

// NameOf returns the stable tag for a concrete Multiplexer implementation.
func NameOf(mux Multiplexer) string {
	switch mux.(type) {
	case *Tmux:
		return "tmux"
	case *Herdr:
		return "herdr"
	default:
		return ""
	}
}
