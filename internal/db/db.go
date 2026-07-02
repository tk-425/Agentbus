// Package db backs the shared agentbus registry and message history with a
// single SQLite file (~/.agentbus/agentbus.db). It is the shared source of
// truth that lets brokers in different projects resolve each other's Agent
// instances by (project, name).
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// Open opens (creating if absent) the SQLite database at path in WAL mode,
// creating the parent directory if missing. WAL lets multiple brokers read and
// write the shared file concurrently.
func Open(path string) (*sql.DB, error) {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create db dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("enable WAL: %w", err)
	}
	return db, nil
}
