package multiplexer

import "sync"

// Mock is an in-memory Multiplexer for tests. Idle state is settable per pane,
// every Inject is appended to a per-pane log, and Capture returns a programmable
// string. It never blocks.
type Mock struct {
	mu        sync.Mutex
	idle      map[string]bool     // paneID -> idle
	idleAfter map[string]int      // paneID -> IsIdle calls to report busy before flipping to idle
	idleCalls map[string]int      // paneID -> count of IsIdle calls
	lastIdle  map[string]bool     // paneID -> idle value the most recent IsIdle returned
	captures  map[string]string   // paneID -> output Capture returns
	injected  map[string][]string // paneID -> append-only injection log
	injIdle   map[string][]bool   // paneID -> idle state observed at each Inject
	panes     []Pane
}

// NewMock returns an empty Mock.
func NewMock() *Mock {
	return &Mock{
		idle:      map[string]bool{},
		idleAfter: map[string]int{},
		idleCalls: map[string]int{},
		lastIdle:  map[string]bool{},
		captures:  map[string]string{},
		injected:  map[string][]string{},
		injIdle:   map[string][]bool{},
	}
}

// SetIdle sets the idle state for a pane.
func (m *Mock) SetIdle(paneID string, idle bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idle[paneID] = idle
}

// SetIdleAfter makes the pane report busy for the first n IsIdle calls, then
// report its SetIdle value — modelling an Agent instance that finishes a task
// and goes Idle between polls.
func (m *Mock) SetIdleAfter(paneID string, n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleAfter[paneID] = n
}

// IdleCalls returns how many times IsIdle was called for a pane.
func (m *Mock) IdleCalls(paneID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idleCalls[paneID]
}

// InjectedWhileIdle returns, per Inject call, the idle state the pane reported
// just before that injection.
func (m *Mock) InjectedWhileIdle(paneID string) []bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]bool, len(m.injIdle[paneID]))
	copy(out, m.injIdle[paneID])
	return out
}

// SetCapture sets the string Capture will return for a pane.
func (m *Mock) SetCapture(paneID, output string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.captures[paneID] = output
}

// Injected returns a copy of the injection log for a pane.
func (m *Mock) Injected(paneID string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, len(m.injected[paneID]))
	copy(out, m.injected[paneID])
	return out
}

// Inject records text in the pane's injection log along with the idle state the
// pane most recently reported, so a test can assert injection happened only
// while Idle.
func (m *Mock) Inject(paneID, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.injected[paneID] = append(m.injected[paneID], text)
	m.injIdle[paneID] = append(m.injIdle[paneID], m.lastIdle[paneID])
	return nil
}

// Capture returns the programmed output for the pane.
func (m *Mock) Capture(paneID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.captures[paneID], nil
}

// IsIdle reports the pane's idle state. While the call count is within the
// SetIdleAfter window the pane reports busy; after that it reports its SetIdle
// value. The returned value is remembered for InjectedWhileIdle.
func (m *Mock) IsIdle(paneID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleCalls[paneID]++
	idle := m.idle[paneID]
	if m.idleCalls[paneID] <= m.idleAfter[paneID] {
		idle = false
	}
	m.lastIdle[paneID] = idle
	return idle, nil
}

// ListPanes returns the configured panes.
func (m *Mock) ListPanes() ([]Pane, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Pane, len(m.panes))
	copy(out, m.panes)
	return out, nil
}
