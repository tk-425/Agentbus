package db

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/tk-425/agentbus/internal/message"
)

// schema creates the shared tables idempotently. brokers tracks each project's
// running broker; agents is the cross-broker registry keyed by (project, name);
// messages is the request/reply history.
const schema = `
CREATE TABLE IF NOT EXISTS brokers (
	project_root TEXT PRIMARY KEY,
	port         INTEGER,
	multiplexer  TEXT,
	pid          INTEGER
);

CREATE TABLE IF NOT EXISTS agents (
	project       TEXT,
	name          TEXT,
	broker_port   INTEGER,
	pane_id       TEXT,
	registered_at TEXT,
	PRIMARY KEY (project, name)
);

CREATE TABLE IF NOT EXISTS messages (
	id         TEXT PRIMARY KEY,
	kind       TEXT,
	from_agent TEXT,
	to_agent   TEXT,
	body       TEXT,
	reply_to   TEXT,
	created_at TEXT,
	read       INTEGER
);
`

// Migrate creates the brokers, agents, and messages tables if they do not
// already exist. It is idempotent: running it on an up-to-date database is a
// no-op.
func Migrate(db *sql.DB) error {
	if _, err := db.Exec(schema); err != nil {
		return fmt.Errorf("migrate schema: %w", err)
	}
	return nil
}

// RecordMessage appends one truncated Request or Reply to durable history when
// the broker has a shared DB attached.
func RecordMessage(db *sql.DB, msg message.Message) error {
	if db == nil {
		return nil
	}
	_, err := db.Exec(`
		INSERT INTO messages (id, kind, from_agent, to_agent, body, reply_to, created_at, read)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`,
		msg.ID,
		string(msg.Kind),
		msg.From,
		msg.To,
		msg.Body,
		msg.ReplyTo,
		msg.CreatedAt.UTC().Format(time.RFC3339Nano),
		0,
	)
	if err != nil {
		return fmt.Errorf("record message %q: %w", msg.ID, err)
	}
	return nil
}

// RecentMessages returns the most recent durable Request/Reply history rows,
// newest first.
func RecentMessages(db *sql.DB, limit int) ([]message.Message, error) {
	if db == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := db.Query(`
		SELECT id, kind, from_agent, to_agent, body, reply_to, created_at
		FROM messages
		ORDER BY created_at DESC, rowid DESC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent messages: %w", err)
	}
	defer rows.Close()

	out := make([]message.Message, 0, limit)
	for rows.Next() {
		var msg message.Message
		var kind string
		var createdAt string
		if err := rows.Scan(&msg.ID, &kind, &msg.From, &msg.To, &msg.Body, &msg.ReplyTo, &createdAt); err != nil {
			return nil, fmt.Errorf("scan recent message: %w", err)
		}
		msg.Kind = message.Kind(kind)
		msg.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, fmt.Errorf("parse message time %q: %w", createdAt, err)
		}
		out = append(out, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent messages: %w", err)
	}
	return out, nil
}
