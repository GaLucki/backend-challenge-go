package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type transactionBeginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}
type unitOfWork struct{ db transactionBeginner }

func NewUnitOfWork(pool *pgxpool.Pool) ports.UnitOfWork { return &unitOfWork{db: pool} }

func (u *unitOfWork) WithinTransaction(ctx context.Context, fn func(ports.Repositories) error) (result error) {
	// The idempotency wait-then-read protocol requires a fresh statement snapshot.
	tx, err := u.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return errors.Join(ports.ErrTransaction, mapError(err))
	}
	// Runs on success, error and panic. A canceled caller must not prevent cleanup.
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		err := tx.Rollback(cleanupCtx)
		if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
			result = errors.Join(result, ports.ErrTransaction, mapError(err))
		}
	}()
	if err := fn(repositories(tx, true)); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.Join(ports.ErrTransaction, mapError(err))
	}
	return nil
}
