package postgres

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type Repository struct {
	pool *pgxpool.Pool
	exec executor
}

func NewRepository(ctx context.Context, databaseURL string) (*Repository, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return &Repository{pool: pool, exec: pool}, nil
}
func (r *Repository) Close()                         { r.pool.Close() }
func (r *Repository) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }
func (r *Repository) WithTx(ctx context.Context, fn func(context.Context, *Repository) error) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	bound := &Repository{pool: r.pool, exec: tx}
	if err := fn(ctx, bound); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx)
}
