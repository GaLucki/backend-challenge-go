package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func TestMapError(t *testing.T) {
	for _, tc := range []struct{ source, errorClass error }{
		{pgx.ErrNoRows, ports.ErrNotFound},
		{&pgconn.PgError{Code: "23505"}, ports.ErrUniqueViolation},
		{&pgconn.PgError{Code: "23514"}, ports.ErrConstraintViolation},
		{&pgconn.PgError{Code: "23503"}, ports.ErrConstraintViolation},
		{&pgconn.PgError{Code: "40001"}, ports.ErrTransaction},
		{&pgconn.PgError{Code: "40P01"}, ports.ErrTransaction},
		{pgx.ErrTxClosed, ports.ErrTransaction},
		{context.Canceled, context.Canceled},
		{context.DeadlineExceeded, context.DeadlineExceeded},
		{errors.New("technical detail"), ports.ErrPersistence},
	} {
		if got := mapError(tc.source); !errors.Is(got, tc.errorClass) {
			t.Errorf("%v: got %v", tc.source, got)
		}
	}
	if mapError(nil) != nil {
		t.Fatal("nil must remain nil")
	}
}
func TestWalletMapping(t *testing.T) {
	r := walletRow{id: "wallet", playerID: "player", currency: "BRL", balance: 2500, version: 7}
	w, err := r.domain()
	if err != nil || w.Balance().Cents() != 2500 || w.Version() != 7 || w.ID() != "wallet" || w.PlayerID() != "player" {
		t.Fatalf("wallet=%v err=%v", w, err)
	}
	r.balance = -1
	if _, err := r.domain(); !errors.Is(err, wallet.ErrNegativeBalance) {
		t.Fatal(err)
	}
	r.balance = 0
	r.version = 0
	if _, err := r.domain(); !errors.Is(err, wallet.ErrInvalidVersion) {
		t.Fatal(err)
	}
	r.currency = "bad"
	if _, err := r.domain(); err == nil {
		t.Fatal("invalid currency accepted")
	}
}
func TestWagerMapping(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := wagerRow{id: "tx", provider: "provider", external: "ext", player: "player", wallet: "wallet", currency: "BRL", kind: "BET", round: "round", reference: "ref", state: "REJECTED", failure: "FUNDS", amount: 100, created: at, updated: at.Add(time.Second)}
	tx, err := r.domain()
	if err != nil || tx.State() != wager.StateRejected || tx.FailureCode() != "FUNDS" || tx.Amount().Cents() != 100 || tx.ReferenceExternalTransactionID() != "ref" || !tx.CreatedAt().Equal(at) || !tx.UpdatedAt().Equal(at.Add(time.Second)) {
		t.Fatalf("tx=%v err=%v", tx, err)
	}
	r.amount = -1
	if _, err := r.domain(); !errors.Is(err, wager.ErrInvalidAmount) {
		t.Fatal(err)
	}
	r.amount = 1
	r.state = "INVALID"
	if _, err := r.domain(); !errors.Is(err, wager.ErrInvalidState) {
		t.Fatal(err)
	}
}
func TestLockAndUpdateRequireTransaction(t *testing.T) {
	r := walletRepository{}
	if _, err := r.GetForUpdate(context.Background(), "wallet"); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if err := r.Update(context.Background(), wallet.Wallet{}, 1); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	w := wagerRepository{}
	if err := w.Update(context.Background(), wager.Transaction{}, wager.StatePending); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	i := idempotencyRepository{}
	if _, err := i.Claim(context.Background(), "key", "hash", time.Now()); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if err := i.Complete(context.Background(), ports.IdempotencyRecord{}); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if err := i.UpdatePendingResult(context.Background(), "tx", nil, time.Now()); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	reverse := reversalRepository{}
	if _, err := reverse.Claim(context.Background(), ports.Reversal{}); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	pending := pendingReferenceRepository{}
	if err := pending.Create(context.Background(), ports.PendingReference{}); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if _, err := pending.ClaimNext(context.Background(), time.Now()); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if err := pending.Update(context.Background(), ports.PendingReference{}); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	inbox := inboxRepository{}
	if _, err := inbox.Claim(context.Background(), ports.InboxMessage{}); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
	if _, err := inbox.GetForUpdate(context.Background(), "consumer", "message"); !errors.Is(err, ports.ErrTransactionRequired) {
		t.Fatal(err)
	}
}

type fakeBeginner struct {
	tx      pgx.Tx
	err     error
	options *pgx.TxOptions
}

func (b fakeBeginner) BeginTx(_ context.Context, options pgx.TxOptions) (pgx.Tx, error) {
	if b.options != nil {
		*b.options = options
	}
	return b.tx, b.err
}

type fakeTx struct {
	pgx.Tx
	commits, rollbacks     int
	commitErr, rollbackErr error
	cleanupCanceled        bool
}

func (tx *fakeTx) Commit(context.Context) error { tx.commits++; return tx.commitErr }
func (tx *fakeTx) Rollback(ctx context.Context) error {
	tx.rollbacks++
	tx.cleanupCanceled = ctx.Err() != nil
	return tx.rollbackErr
}
func TestUnitOfWorkLifecycle(t *testing.T) {
	sentinel := errors.New("callback failure")
	for _, tc := range []struct {
		name                                string
		callbackErr, commitErr, rollbackErr error
		wantCommit                          int
		wantErr                             error
	}{
		{name: "commit", wantCommit: 1},
		{name: "callback rollback", callbackErr: sentinel, wantErr: sentinel},
		{name: "commit failure", commitErr: pgx.ErrTxCommitRollback, wantCommit: 1, wantErr: ports.ErrTransaction},
		{name: "cleanup failure", callbackErr: sentinel, rollbackErr: errors.New("network"), wantErr: ports.ErrTransaction},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := &fakeTx{commitErr: tc.commitErr, rollbackErr: tc.rollbackErr}
			var options pgx.TxOptions
			u := unitOfWork{db: fakeBeginner{tx: tx, options: &options}}
			err := u.WithinTransaction(context.Background(), func(rs ports.Repositories) error {
				for _, r := range []repository{rs.Wallets.(*walletRepository).repository, rs.Wagers.(*wagerRepository).repository, rs.Ledger.(*ledgerRepository).repository, rs.Inbox.(*inboxRepository).repository, rs.Outbox.(*outboxRepository).repository, rs.Idempotency.(*idempotencyRepository).repository, rs.Reversals.(*reversalRepository).repository, rs.PendingReferences.(*pendingReferenceRepository).repository} {
					if r.db != tx || !r.transactional {
						t.Fatal("repository does not share pgx.Tx")
					}
				}
				return tc.callbackErr
			})
			if !errors.Is(err, tc.wantErr) || tx.commits != tc.wantCommit || tx.rollbacks != 1 {
				t.Fatalf("err=%v commits=%d rollbacks=%d", err, tx.commits, tx.rollbacks)
			}
			if options.IsoLevel != pgx.ReadCommitted {
				t.Fatal("idempotency requires READ COMMITTED")
			}
		})
	}
	u := unitOfWork{db: fakeBeginner{err: errors.New("begin failed")}}
	if err := u.WithinTransaction(context.Background(), func(ports.Repositories) error { t.Fatal("callback called"); return nil }); !errors.Is(err, ports.ErrTransaction) {
		t.Fatal(err)
	}
}
func TestUnitOfWorkPanicAndCanceledCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tx := &fakeTx{}
	u := unitOfWork{db: fakeBeginner{tx: tx}}
	func() {
		defer func() {
			if recover() != "panic" {
				t.Error("panic not preserved")
			}
		}()
		_ = u.WithinTransaction(ctx, func(ports.Repositories) error { cancel(); panic("panic") })
	}()
	if tx.rollbacks != 1 || tx.commits != 0 || tx.cleanupCanceled {
		t.Fatalf("unexpected cleanup: %+v", tx)
	}
}
