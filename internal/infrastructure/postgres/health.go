package postgres

import "context"

// PostgresChecker exposes the database health through the readiness contract.
// Name and Check are intentionally independent from any transport package so
// the HTTP adapter can consume it through a local interface.
type PostgresChecker struct{ db *Repository }

func NewPostgresChecker(db *Repository) *PostgresChecker {
	return &PostgresChecker{db: db}
}

func (c *PostgresChecker) Name() string { return "postgres" }

func (c *PostgresChecker) Check(ctx context.Context) error { return c.db.Ping(ctx) }
