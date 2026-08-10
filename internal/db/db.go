// Package db wires up the Postgres connection used by the rest of
// the app. Everything else in the codebase should depend on
// *sqlx.DB, never on a raw *sql.DB.
package db

import (
	"time"

	"github.com/jmoiron/sqlx"

	// Registers the "postgres" driver with database/sql.
	_ "github.com/lib/pq"
)

// Open dials Postgres using lib/pq, verifies the connection with a
// Ping, and returns a *sqlx.DB configured with pool limits sized for
// Supabase's transaction-mode pooler (which keeps per-connection
// state minimal, so a small pool is appropriate).
func Open(dsn string) (*sqlx.DB, error) {
	db, err := sqlx.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}
