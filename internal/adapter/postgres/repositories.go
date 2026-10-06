package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type executor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}
type repository struct {
	db            executor
	transactional bool
}
type walletRepository struct{ repository }
type wagerRepository struct{ repository }
type ledgerRepository struct{ repository }
type inboxRepository struct{ repository }
type outboxRepository struct{ repository }
type idempotencyRepository struct{ repository }
type reversalRepository struct{ repository }
type pendingReferenceRepository struct{ repository }

func repositories(db executor, transactional bool) ports.Repositories {
	r := repository{db: db, transactional: transactional}
	return ports.Repositories{Wallets: &walletRepository{r}, Wagers: &wagerRepository{r}, Ledger: &ledgerRepository{r}, Inbox: &inboxRepository{r}, Outbox: &outboxRepository{r}, Idempotency: &idempotencyRepository{r}, Reversals: &reversalRepository{r}, PendingReferences: &pendingReferenceRepository{r}}
}

// NewRepositories uses the existing pool for standalone reads/inserts.
// Financial operations must use the repositories supplied by UnitOfWork.
func NewRepositories(pool *pgxpool.Pool) ports.Repositories { return repositories(pool, false) }

func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ports.ErrNotFound
	}
	if errors.Is(err, pgx.ErrTxClosed) || errors.Is(err, pgx.ErrTxCommitRollback) {
		return ports.ErrTransaction
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505":
			return ports.ErrUniqueViolation
		case "23502", "23503", "23514", "23P01", "22P02", "22003":
			return ports.ErrConstraintViolation
		case "40001", "40P01", "25P02":
			return ports.ErrTransaction
		}
	}
	return ports.ErrPersistence
}

func affected(tag pgconn.CommandTag, err error, absent error) error {
	if err != nil {
		return mapError(err)
	}
	if tag.RowsAffected() == 0 {
		return absent
	}
	return nil
}

type walletRow struct {
	id, playerID, currency string
	balance, version       int64
}

func (r walletRow) domain() (wallet.Wallet, error) {
	m, err := money.New(r.balance, r.currency)
	if err != nil {
		return wallet.Wallet{}, fmt.Errorf("rehydrate wallet money: %w", err)
	}
	return wallet.Rehydrate(wallet.ID(r.id), wallet.PlayerID(r.playerID), r.currency, m, r.version)
}
func scanWallet(row pgx.Row) (wallet.Wallet, error) {
	var r walletRow
	if err := row.Scan(&r.id, &r.playerID, &r.currency, &r.balance, &r.version); err != nil {
		return wallet.Wallet{}, mapError(err)
	}
	return r.domain()
}
func (r *walletRepository) Create(ctx context.Context, w wallet.Wallet) error {
	_, err := r.db.Exec(ctx, `INSERT INTO wallets(id,player_id,currency,balance_cents,version) VALUES($1,$2,$3,$4,$5)`, string(w.ID()), string(w.PlayerID()), w.Currency(), w.Balance().Cents(), w.Version())
	return mapError(err)
}
func (r *walletRepository) Get(ctx context.Context, id wallet.ID) (wallet.Wallet, error) {
	return scanWallet(r.db.QueryRow(ctx, `SELECT id,player_id,currency,balance_cents,version FROM wallets WHERE id=$1`, string(id)))
}
func (r *walletRepository) GetForUpdate(ctx context.Context, id wallet.ID) (wallet.Wallet, error) {
	if !r.transactional {
		return wallet.Wallet{}, ports.ErrTransactionRequired
	}
	return scanWallet(r.db.QueryRow(ctx, `SELECT id,player_id,currency,balance_cents,version FROM wallets WHERE id=$1 FOR UPDATE`, string(id)))
}

// Update requires the version observed before domain operations, preventing lost updates.
func (r *walletRepository) Update(ctx context.Context, w wallet.Wallet, expectedVersion int64) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	return affectedExec(ctx, r.db, `UPDATE wallets SET balance_cents=$2,version=$3,updated_at=now() WHERE id=$1 AND version=$4 AND player_id=$5 AND currency=$6 AND $3 > $4`, ports.ErrConflict, string(w.ID()), w.Balance().Cents(), w.Version(), expectedVersion, string(w.PlayerID()), w.Currency())
}
func affectedExec(ctx context.Context, db executor, sql string, absent error, args ...any) error {
	tag, err := db.Exec(ctx, sql, args...)
	return affected(tag, err, absent)
}

type wagerRow struct {
	id, provider, external, player, wallet, currency, kind, round, reference, state, failure string
	amount                                                                                   int64
	created, updated                                                                         time.Time
}

func (r wagerRow) domain() (wager.Transaction, error) {
	amount, err := money.New(r.amount, r.currency)
	if err != nil {
		return wager.Transaction{}, fmt.Errorf("rehydrate wager money: %w", err)
	}
	return wager.Rehydrate(wager.PersistedState{
		ExternalParams: wager.ExternalParams{ID: wager.TransactionID(r.id), ProviderID: wager.ProviderID(r.provider), ExternalTransactionID: wager.ExternalTransactionID(r.external), PlayerID: wager.PlayerID(r.player), WalletID: wager.WalletID(r.wallet), Type: wager.Type(r.kind), Amount: amount, RoundID: wager.RoundID(r.round), ReferenceExternalTransactionID: wager.ExternalTransactionID(r.reference)},
		State:          wager.State(r.state), FailureCode: wager.FailureCode(r.failure), CreatedAt: r.created, UpdatedAt: r.updated,
	})
}

const wagerColumns = `transaction_id,coalesce(provider_id,''),coalesce(external_transaction_id,''),player_id,wallet_id,currency,type,amount_cents,coalesce(round_id,''),coalesce(reference_external_transaction_id,''),state,coalesce(failure_code,''),created_at,updated_at`

func scanWager(row pgx.Row) (wager.Transaction, error) {
	var r wagerRow
	if err := row.Scan(&r.id, &r.provider, &r.external, &r.player, &r.wallet, &r.currency, &r.kind, &r.amount, &r.round, &r.reference, &r.state, &r.failure, &r.created, &r.updated); err != nil {
		return wager.Transaction{}, mapError(err)
	}
	return r.domain()
}
func (r *wagerRepository) Create(ctx context.Context, t wager.Transaction) error {
	_, err := r.db.Exec(ctx, `INSERT INTO wager_transactions(transaction_id,provider_id,external_transaction_id,player_id,wallet_id,currency,type,amount_cents,round_id,reference_external_transaction_id,state,failure_code,created_at,updated_at) VALUES($1,NULLIF($2,''),NULLIF($3,''),$4,$5,$6,$7,$8,NULLIF($9,''),NULLIF($10,''),$11,NULLIF($12,''),$13,$14)`, string(t.ID()), string(t.ProviderID()), string(t.ExternalTransactionID()), string(t.PlayerID()), string(t.WalletID()), t.Currency(), string(t.Type()), t.Amount().Cents(), string(t.RoundID()), string(t.ReferenceExternalTransactionID()), string(t.State()), string(t.FailureCode()), t.CreatedAt(), t.UpdatedAt())
	return mapError(err)
}
func (r *wagerRepository) Get(ctx context.Context, id wager.TransactionID) (wager.Transaction, error) {
	return scanWager(r.db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE transaction_id=$1`, string(id)))
}
func (r *wagerRepository) GetByExternalID(ctx context.Context, provider wager.ProviderID, external wager.ExternalTransactionID) (wager.Transaction, error) {
	return scanWager(r.db.QueryRow(ctx, `SELECT `+wagerColumns+` FROM wager_transactions WHERE provider_id=$1 AND external_transaction_id=$2`, string(provider), string(external)))
}

// Only mutable processing state is saved; expectedState prevents a stale overwrite.
func (r *wagerRepository) Update(ctx context.Context, t wager.Transaction, expectedState wager.State) error {
	if !r.transactional {
		return ports.ErrTransactionRequired
	}
	return affectedExec(ctx, r.db, `UPDATE wager_transactions SET state=$2,failure_code=NULLIF($3,''),updated_at=$4 WHERE transaction_id=$1 AND state=$5 AND state IN ('PENDING','PENDING_REFERENCE')`, ports.ErrConflict, string(t.ID()), string(t.State()), string(t.FailureCode()), t.UpdatedAt(), string(expectedState))
}

func (r *ledgerRepository) Append(ctx context.Context, e ports.LedgerEntry) error {
	_, err := r.db.Exec(ctx, `INSERT INTO wallet_ledger_entries(id,wallet_id,transaction_id,direction,amount_cents,balance_before_cents,balance_after_cents,wallet_version,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, e.ID, string(e.WalletID), string(e.TransactionID), string(e.Direction), e.AmountCents, e.BalanceBeforeCents, e.BalanceAfterCents, e.WalletVersion, e.CreatedAt)
	return mapError(err)
}
func (r *ledgerRepository) Get(ctx context.Context, id string) (ports.LedgerEntry, error) {
	var e ports.LedgerEntry
	var walletID, txID, direction string
	err := r.db.QueryRow(ctx, `SELECT id,wallet_id,transaction_id,direction,amount_cents,balance_before_cents,balance_after_cents,wallet_version,created_at FROM wallet_ledger_entries WHERE id=$1`, id).Scan(&e.ID, &walletID, &txID, &direction, &e.AmountCents, &e.BalanceBeforeCents, &e.BalanceAfterCents, &e.WalletVersion, &e.CreatedAt)
	e.WalletID, e.TransactionID, e.Direction = wallet.ID(walletID), wager.TransactionID(txID), ports.LedgerDirection(direction)
	return e, mapError(err)
}
func (r *inboxRepository) Create(ctx context.Context, m ports.InboxMessage) error {
	_, err := r.db.Exec(ctx, `INSERT INTO inbox_messages(consumer_name,message_id,payload_hash,received_at,completed_at) VALUES($1,$2,$3,$4,$5)`, m.ConsumerName, m.MessageID, m.PayloadHash, m.ReceivedAt, m.CompletedAt)
	return mapError(err)
}
func (r *inboxRepository) Get(ctx context.Context, consumer, id string) (ports.InboxMessage, error) {
	var m ports.InboxMessage
	err := r.db.QueryRow(ctx, `SELECT consumer_name,message_id,payload_hash,received_at,completed_at FROM inbox_messages WHERE consumer_name=$1 AND message_id=$2`, consumer, id).Scan(&m.ConsumerName, &m.MessageID, &m.PayloadHash, &m.ReceivedAt, &m.CompletedAt)
	return m, mapError(err)
}
func (r *inboxRepository) Complete(ctx context.Context, consumer, id string, at time.Time) error {
	return affectedExec(ctx, r.db, `UPDATE inbox_messages SET completed_at=$3 WHERE consumer_name=$1 AND message_id=$2`, ports.ErrNotFound, consumer, id, at)
}
func (r *outboxRepository) Create(ctx context.Context, e ports.OutboxEvent) error {
	_, err := r.db.Exec(ctx, `INSERT INTO outbox_events(event_id,aggregate_id,event_type,payload,occurred_at,retry_count,next_attempt_at,published_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, e.EventID, e.AggregateID, e.EventType, []byte(e.Payload), e.OccurredAt, e.RetryCount, e.NextAttemptAt, e.PublishedAt)
	return mapError(err)
}
func (r *outboxRepository) Get(ctx context.Context, id string) (ports.OutboxEvent, error) {
	var e ports.OutboxEvent
	var payload []byte
	err := r.db.QueryRow(ctx, `SELECT event_id,aggregate_id,event_type,payload,occurred_at,retry_count,next_attempt_at,published_at FROM outbox_events WHERE event_id=$1`, id).Scan(&e.EventID, &e.AggregateID, &e.EventType, &payload, &e.OccurredAt, &e.RetryCount, &e.NextAttemptAt, &e.PublishedAt)
	e.Payload = payload
	return e, mapError(err)
}
