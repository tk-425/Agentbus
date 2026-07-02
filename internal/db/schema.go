package db

import (
	"database/sql"
	"fmt"
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
