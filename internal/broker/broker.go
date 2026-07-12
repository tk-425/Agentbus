package broker

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/tk-425/agentbus/internal/db"
	"github.com/tk-425/agentbus/internal/message"
	"github.com/tk-425/agentbus/internal/multiplexer"
	"github.com/tk-425/agentbus/internal/registry"
)

// Port scan range for Serve: the broker binds the first free loopback port.
const (
	portScanStart = 7373
	portScanEnd   = 7473
)

// maxBodyBytes caps a Message body at 32KB. Larger bodies are truncated once,
// here at the broker — agents should pass file paths for large content.
const maxBodyBytes = 32768

// truncationMarker is appended to a body that exceeded maxBodyBytes.
const truncationMarker = "\n[truncated — pass file paths for large content]"

// Broker is the in-memory message bus: a Registry of Agent instances plus a
// per-agent Queue, optionally exposed over HTTP by Serve. Send enqueues a
// Message for its recipient; Inbox drains a recipient's queue. The broker is the
// single truncation enforcement point — the watcher and queue never truncate.
type Broker struct {
	Registry     *registry.Registry
	queue        *Queue
	correlations *correlations

	db  *sql.DB      // shared store for the brokers row; nil outside Serve
	srv *http.Server // set while Serve is running, for Shutdown
}

// New returns a Broker with an empty registry, queue, and correlation store.
func New() *Broker {
	return &Broker{
		Registry:     registry.New(),
		queue:        NewQueue(),
		correlations: newCorrelations(),
	}
}

// AttachDB lets Serve record this broker's row in the shared brokers table.
// Passing a nil db is a no-op (the in-memory broker runs without persistence).
func (b *Broker) AttachDB(db *sql.DB) {
	b.db = db
}

// Register binds a bare agent type to a pane in project and returns the
// auto-suffixed Agent instance name.
func (b *Broker) Register(project, agentType, paneID string, backend ...string) (string, error) {
	return b.Registry.RegisterType(project, agentType, paneID, backend...)
}

// Send enqueues msg for its To recipient, truncating an over-cap body once.
// When a shared DB is attached, the same boundary also records durable
// Request/Reply history. History is best-effort: a failed write only degrades
// `log` completeness and must never block live delivery, since the inbox is the
// live surface and durable history is a separate concern (spec Key Decisions).
func (b *Broker) Send(msg message.Message) error {
	msg.Body = truncate(msg.Body)
	if err := db.RecordMessage(b.db, msg); err != nil {
		log.Printf("agentbus: record message %s history: %v", msg.ID, err)
	}
	// Correlate a Request to its Requester so a later `agentbus reply <id>` can
	// route the terminal Reply back. Never correlate a Reply: a Reply is terminal
	// and must not become answerable itself, which would reintroduce the loop
	// ADR-0001 prevents (spec Constraints).
	if msg.Kind == message.KindRequest {
		b.correlations.record(msg.ID, msg.From, msg.To)
	}
	b.queue.Enqueue(msg)
	return nil
}

// Reply produces the terminal Reply to the Request identified by id. It looks up
// the correlation recorded when the Request was enqueued, builds a Reply from the
// Recipient back to the original Requester (ReplyTo set to id), and routes it
// through the normal path so a Requester on another broker is forwarded. The
// caller supplies only the id and body; origin and destination come from the
// stored correlation. The correlation is claimed (looked up and removed) in one
// step, so a request ID answers exactly one Reply even under concurrent replies.
// An unrecorded id returns ErrUnknownRequest.
func (b *Broker) Reply(id, body string) error {
	corr, ok := b.correlations.claim(id)
	if !ok {
		return ErrUnknownRequest
	}
	reply := message.Message{
		ID:        message.NewID(),
		Kind:      message.KindReply,
		From:      corr.recipient,
		To:        corr.requester,
		Body:      body,
		ReplyTo:   id,
		CreatedAt: time.Now().UTC(),
	}
	if err := b.Route(reply); err != nil {
		// Routing failed before the Reply was delivered; restore the claim so
		// the recipient can retry rather than losing the request ID to a
		// transient error.
		b.correlations.record(id, corr.requester, corr.recipient)
		return err
	}
	return nil
}

// DefaultPortFile returns the path the broker writes its chosen port to,
// ~/.agentbus/port. Clients read it to find the running broker.
func DefaultPortFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "agentbus-port")
	}
	return filepath.Join(home, ".agentbus", "port")
}

// Serve binds the first free loopback port in [portScanStart, portScanEnd],
// writes it to portFile, records the broker's row in the shared brokers table
// (when a DB is attached), and serves the HTTP handler until Shutdown is called.
// On return it removes the port file and the brokers row. mux identifies the
// multiplexer backend recorded for this broker. Serve blocks.
func (b *Broker) Serve(projectRoot, portFile string, mux multiplexer.Multiplexer) error {
	ln, port, err := listenInRange()
	if err != nil {
		return err
	}

	if err := writePortFile(portFile, port); err != nil {
		ln.Close()
		return err
	}
	defer os.Remove(portFile)

	// Attach the registry before the handler accepts requests, so every agent
	// registered over HTTP write-throughs a row carrying this broker's port —
	// the address peers forward to (Task 7 routing).
	b.Registry.AttachDB(b.db, port)

	if b.db != nil {
		b.db.Exec(`
			INSERT INTO brokers (project_root, port, multiplexer, pid)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(project_root) DO UPDATE SET
				port = excluded.port, multiplexer = excluded.multiplexer, pid = excluded.pid`,
			projectRoot, port, fmt.Sprintf("%T", mux), os.Getpid())
		projectName := filepath.Base(projectRoot)
		defer b.db.Exec(`DELETE FROM agents WHERE project = ?`, projectName)
		defer b.db.Exec(`DELETE FROM brokers WHERE project_root = ?`, projectRoot)
	}

	// ReadHeaderTimeout guards against a slow-header (Slowloris) client even
	// though the listener is loopback-only.
	b.srv = &http.Server{Handler: b.Handler(), ReadHeaderTimeout: 5 * time.Second}
	if err := b.srv.Serve(ln); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops a running Serve.
func (b *Broker) Shutdown(ctx context.Context) error {
	if b.srv == nil {
		return nil
	}
	return b.srv.Shutdown(ctx)
}

// listenInRange returns a listener on the first free loopback port in the scan
// range, along with that port.
func listenInRange() (net.Listener, int, error) {
	for port := portScanStart; port <= portScanEnd; port++ {
		ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
		if err == nil {
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no free port in %d-%d", portScanStart, portScanEnd)
}

func writePortFile(path string, port int) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create port-file dir: %w", err)
		}
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)), 0o600)
}

// truncate caps body at maxBodyBytes, appending truncationMarker when it
// overflows. Bodies at or under the cap pass through unchanged. Truncation is
// idempotent: an already-truncated body re-truncates to the same prefix +
// single marker rather than growing.
func truncate(body string) string {
	if len(body) <= maxBodyBytes {
		return body
	}
	return body[:maxBodyBytes] + truncationMarker
}

// Inbox drains and returns all messages queued for agent.
func (b *Broker) Inbox(agent string) []message.Message {
	return b.queue.Drain(agent)
}

// Requests drains and returns only Request messages queued for agent, leaving
// terminal inbox-only Replies available for a human inbox read.
func (b *Broker) Requests(agent string) []message.Message {
	return b.queue.DrainRequests(agent)
}

// UnnotifiedReplies returns queued Replies for agent whose arrival has not yet
// been announced, without draining them (ADR-0002).
func (b *Broker) UnnotifiedReplies(agent string) []message.Message {
	return b.queue.UnnotifiedReplies(agent)
}

// MarkNotified records that arrival notifications for these message IDs were
// injected.
func (b *Broker) MarkNotified(ids []string) {
	b.queue.MarkNotified(ids)
}
