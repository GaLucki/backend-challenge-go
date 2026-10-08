package financial

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type DivergenceCode string

const (
	BalanceMismatch              DivergenceCode = "BALANCE_MISMATCH"
	LedgerChainBroken            DivergenceCode = "LEDGER_CHAIN_BROKEN"
	LedgerAmountMismatch         DivergenceCode = "LEDGER_AMOUNT_MISMATCH"
	LedgerDirectionMismatch      DivergenceCode = "LEDGER_DIRECTION_MISMATCH"
	LedgerCurrencyMismatch       DivergenceCode = "LEDGER_CURRENCY_MISMATCH"
	LedgerTransactionNotFound    DivergenceCode = "LEDGER_TRANSACTION_NOT_FOUND"
	TransactionLedgerMissing     DivergenceCode = "TRANSACTION_LEDGER_MISSING"
	UnexpectedLedgerEntry        DivergenceCode = "UNEXPECTED_LEDGER_ENTRY"
	NegativeReconstructedBalance DivergenceCode = "NEGATIVE_RECONSTRUCTED_BALANCE"
	WalletVersionMismatch        DivergenceCode = "WALLET_VERSION_MISMATCH"
	TransactionWalletMismatch    DivergenceCode = "TRANSACTION_WALLET_MISMATCH"
	DuplicateLedgerEntry         DivergenceCode = "DUPLICATE_LEDGER_ENTRY"
	LedgerArithmeticOverflow     DivergenceCode = "LEDGER_ARITHMETIC_OVERFLOW"
	TransactionInvalid           DivergenceCode = "TRANSACTION_INVALID"
)

type Divergence struct {
	Code          DivergenceCode      `json:"code"`
	TransactionID wager.TransactionID `json:"transactionId,omitempty"`
	LedgerEntryID string              `json:"ledgerEntryId,omitempty"`
	Expected      string              `json:"expected"`
	Actual        string              `json:"actual"`
	Message       string              `json:"message"`
}

type ReconciliationResult struct {
	StoredBalance     money.External  `json:"storedBalance"`
	CalculatedBalance *money.External `json:"calculatedBalance"`
	Difference        *money.External `json:"difference"`
	Consistent        bool            `json:"consistent"`
	CheckedEntries    int             `json:"checkedEntries"`
	WalletID          wallet.ID       `json:"walletId"`
	Currency          string          `json:"currency"`
	Status            string          `json:"status"`
	WalletBalance     money.External  `json:"walletBalance"`
	// Null means corrupt ledger arithmetic cannot yield an int64 balance.
	LedgerBalance *money.External `json:"ledgerBalance"`
	WalletVersion int64           `json:"walletVersion"`
	// Null means corrupt transaction semantics make derivation unsafe.
	ExpectedWalletVersion *int64       `json:"expectedWalletVersion"`
	LedgerEntriesChecked  int          `json:"ledgerEntriesChecked"`
	TransactionsChecked   int          `json:"transactionsChecked"`
	Divergences           []Divergence `json:"divergences"`
	CheckedAt             time.Time    `json:"checkedAt"`
}

type ReconciliationService struct {
	reader  ports.ReconciliationReader
	auth    *identity.Authorizer
	metrics ports.Telemetry
}

func NewReconciliationService(reader ports.ReconciliationReader, auth *identity.Authorizer) *ReconciliationService {
	return &ReconciliationService{reader: reader, auth: auth}
}

func (s *ReconciliationService) WithTelemetry(t ports.Telemetry) *ReconciliationService {
	s.metrics = t
	return s
}

func (s *ReconciliationService) Reconcile(ctx context.Context, p identity.Principal, id wallet.ID) (result ReconciliationResult, resultErr error) {
	if err := s.auth.RequireInternal(p); err != nil {
		return ReconciliationResult{}, err
	}
	started := time.Now()
	defer func() {
		if s.metrics == nil {
			return
		}
		status := result.Status
		if resultErr != nil {
			status = "ERROR"
		}
		s.metrics.Count("reconciliation_runs_total", status)
		s.metrics.Duration("reconciliation_duration_seconds", time.Since(started), status)
		for _, d := range result.Divergences {
			s.metrics.Count("reconciliation_divergences_total", string(d.Code))
		}
		if errors.Is(resultErr, ErrPersistence) {
			s.metrics.Count("dependency_failures_total", "postgres", "financial")
		}
	}()
	snapshot, err := s.reader.ReadReconciliation(ctx, id)
	if err != nil {
		if errors.Is(err, ports.ErrNotFound) {
			return ReconciliationResult{}, ErrWalletNotFound
		}
		return ReconciliationResult{}, applicationError(err)
	}
	return auditSnapshot(ctx, snapshot)
}

type auditMovement struct {
	direction ports.LedgerDirection
	required  bool
	valid     bool
}

// Reversals use the same reference validator as normal processing and the
// pending worker; reconciliation never calls a mutation or replays operations.
func expectedMovement(t ports.AuditTransaction, refs map[[2]string]ports.AuditTransaction) auditMovement {
	switch t.State {
	case wager.StatePending, wager.StatePendingReference, wager.StateRejected, wager.StateFailed:
		return auditMovement{valid: true}
	case wager.StateProcessed:
	default:
		return auditMovement{}
	}
	switch t.Type {
	case wager.TypeLoss:
		return auditMovement{valid: t.AmountCents == 0}
	case wager.TypeOpening:
		return auditMovement{direction: ports.Credit, required: true, valid: t.AmountCents > 0}
	case wager.TypeWin:
		valid := t.AmountCents > 0
		if t.ReferenceExternalID != "" {
			ref, exists := refs[[2]string{string(t.ProviderID), string(t.ReferenceExternalID)}]
			valid = valid && exists && ref.Type == wager.TypeBet && ref.State == wager.StateProcessed &&
				ref.PlayerID == t.PlayerID && ref.WalletID == t.WalletID && ref.Currency == t.Currency && ref.RoundID == t.RoundID
		}
		return auditMovement{direction: ports.Credit, required: true, valid: valid}
	case wager.TypeBet:
		return auditMovement{direction: ports.Debit, required: true, valid: t.AmountCents > 0}
	case wager.TypeRefund, wager.TypeRollback:
		ref, ok := refs[[2]string{string(t.ProviderID), string(t.ReferenceExternalID)}]
		if !ok || t.ExternalID == t.ReferenceExternalID {
			return auditMovement{required: true}
		}
		tx, e1 := auditDomainTransaction(t)
		original, e2 := auditDomainTransaction(ref)
		if e1 != nil || e2 != nil {
			return auditMovement{required: true}
		}
		direction, failure := validateReference(tx, original)
		return auditMovement{direction: direction, required: true, valid: failure == "" && t.AmountCents > 0}
	default:
		return auditMovement{}
	}
}

func auditDomainTransaction(t ports.AuditTransaction) (wager.Transaction, error) {
	m, err := money.New(t.AmountCents, t.Currency)
	if err != nil {
		return wager.Transaction{}, err
	}
	return wager.Rehydrate(wager.PersistedState{ExternalParams: wager.ExternalParams{ID: t.ID, ProviderID: t.ProviderID, ExternalTransactionID: t.ExternalID, PlayerID: t.PlayerID, WalletID: t.WalletID, Type: t.Type, Amount: m, RoundID: t.RoundID, ReferenceExternalTransactionID: t.ReferenceExternalID}, State: t.State, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0)})
}

func auditSnapshot(ctx context.Context, s ports.ReconciliationSnapshot) (ReconciliationResult, error) {
	w := s.Wallet
	r := ReconciliationResult{WalletID: w.ID(), Currency: w.Currency(), Status: "CONSISTENT", WalletBalance: w.Balance().External(), WalletVersion: w.Version(), LedgerEntriesChecked: len(s.Ledger), TransactionsChecked: len(s.Transactions), Divergences: make([]Divergence, 0)}
	add := func(code DivergenceCode, tx wager.TransactionID, entry, expected, actual, message string) {
		r.Divergences = append(r.Divergences, Divergence{code, tx, entry, expected, actual, message})
	}
	byID := make(map[wager.TransactionID]ports.AuditTransaction, len(s.Transactions))
	refs := make(map[[2]string]ports.AuditTransaction, len(s.Transactions))
	for _, t := range s.Transactions {
		if err := ctx.Err(); err != nil {
			return ReconciliationResult{}, err
		}
		byID[t.ID] = t
		if t.ProviderID != "" && t.ExternalID != "" {
			refs[[2]string{string(t.ProviderID), string(t.ExternalID)}] = t
		}
	}
	entries := append([]ports.LedgerEntry(nil), s.Ledger...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].WalletVersion != entries[j].WalletVersion {
			return entries[i].WalletVersion < entries[j].WalletVersion
		}
		return entries[i].ID < entries[j].ID
	})
	counts := make(map[wager.TransactionID]int)
	balance, _ := money.Zero(w.Currency())
	reconstructable := true
	previous := int64(0)
	ledgerVersion := int64(1)
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return ReconciliationResult{}, err
		}
		counts[e.TransactionID]++
		if counts[e.TransactionID] > 1 {
			add(DuplicateLedgerEntry, e.TransactionID, e.ID, "1 entry per transaction", strconv.Itoa(counts[e.TransactionID]), "transaction has duplicate ledger movements")
		}
		if e.BalanceBeforeCents != previous {
			add(LedgerChainBroken, e.TransactionID, e.ID, strconv.FormatInt(previous, 10), strconv.FormatInt(e.BalanceBeforeCents, 10), "balance before does not match preceding balance after")
		}
		previous = e.BalanceAfterCents
		t, exists := byID[e.TransactionID]
		if !exists {
			add(LedgerTransactionNotFound, e.TransactionID, e.ID, "existing transaction", "missing", "ledger transaction does not exist")
		} else {
			if t.WalletID != wager.WalletID(w.ID()) || e.WalletID != w.ID() {
				add(TransactionWalletMismatch, t.ID, e.ID, string(w.ID()), string(t.WalletID), "transaction and ledger must belong to the audited wallet")
			}
			if t.Currency != w.Currency() {
				add(LedgerCurrencyMismatch, t.ID, e.ID, w.Currency(), t.Currency, "transaction currency differs from implicit ledger currency")
			}
			if t.AmountCents != e.AmountCents {
				add(LedgerAmountMismatch, t.ID, e.ID, strconv.FormatInt(t.AmountCents, 10), strconv.FormatInt(e.AmountCents, 10), "ledger amount differs from transaction amount")
			}
			movement := expectedMovement(t, refs)
			if !movement.required || !movement.valid {
				add(UnexpectedLedgerEntry, t.ID, e.ID, "valid processed financial movement", string(t.State)+":"+string(t.Type), "transaction does not authorize this ledger movement")
			}
			if movement.direction != "" && movement.direction != e.Direction {
				add(LedgerDirectionMismatch, t.ID, e.ID, string(movement.direction), string(e.Direction), "ledger direction differs from operation semantics")
			}
		}
		if exists && t.Type == wager.TypeOpening {
			if i != 0 {
				add(UnexpectedLedgerEntry, t.ID, e.ID, "OPENING as first movement", strconv.Itoa(i+1), "opening must precede all other movements")
			}
		} else {
			ledgerVersion++
		}
		if e.WalletVersion != ledgerVersion {
			add(WalletVersionMismatch, e.TransactionID, e.ID, strconv.FormatInt(ledgerVersion, 10), strconv.FormatInt(e.WalletVersion, 10), "ledger movement version is out of sequence")
		}
		amount, _ := money.New(e.AmountCents, w.Currency())
		before, _ := money.New(e.BalanceBeforeCents, w.Currency())
		apply := func(value money.Money) (money.Money, error) {
			if e.Direction == ports.Credit {
				return value.Add(amount)
			}
			return value.Sub(amount)
		}
		if e.AmountCents <= 0 {
			add(LedgerAmountMismatch, e.TransactionID, e.ID, "positive cents", strconv.FormatInt(e.AmountCents, 10), "ledger amount must be positive")
			reconstructable = false
		}
		if e.Direction != ports.Credit && e.Direction != ports.Debit {
			add(LedgerDirectionMismatch, e.TransactionID, e.ID, "CREDIT or DEBIT", string(e.Direction), "ledger direction is invalid")
			reconstructable = false
			continue
		}
		calculated, err := apply(before)
		if err != nil {
			add(LedgerArithmeticOverflow, e.TransactionID, e.ID, "representable int64 cents", "overflow", "ledger entry arithmetic exceeds monetary range")
		} else if calculated.Cents() != e.BalanceAfterCents {
			add(LedgerChainBroken, e.TransactionID, e.ID, strconv.FormatInt(calculated.Cents(), 10), strconv.FormatInt(e.BalanceAfterCents, 10), "balance after differs from movement arithmetic")
		}
		if e.BalanceBeforeCents < 0 || e.BalanceAfterCents < 0 {
			add(NegativeReconstructedBalance, e.TransactionID, e.ID, "non-negative balance", "negative", "ledger contains a negative balance")
		}
		if reconstructable {
			next, err := apply(balance)
			if err != nil {
				add(LedgerArithmeticOverflow, e.TransactionID, e.ID, "representable int64 cents", "overflow", "reconstructed balance exceeds monetary range")
				reconstructable = false
			} else {
				balance = next
				if balance.IsNegative() {
					add(NegativeReconstructedBalance, e.TransactionID, e.ID, "non-negative balance", balance.DecimalString(), "reconstructed balance is negative")
				}
			}
		}
	}
	if reconstructable {
		b := balance.External()
		r.LedgerBalance = &b
		if !balance.Equal(w.Balance()) {
			add(BalanceMismatch, "", "", balance.DecimalString(), w.Balance().DecimalString(), "wallet balance differs from reconstructed ledger balance")
		}
	}
	if previous != w.Balance().Cents() {
		add(BalanceMismatch, "", "", fmt.Sprintf("last ledger balance %d cents", previous), fmt.Sprintf("wallet balance %d cents", w.Balance().Cents()), "wallet balance differs from last ledger balance after")
	}
	version := int64(1)
	versionValid := true
	openings := 0
	transactions := append([]ports.AuditTransaction(nil), s.Transactions...)
	sort.Slice(transactions, func(i, j int) bool { return transactions[i].ID < transactions[j].ID })
	for _, t := range transactions {
		if err := ctx.Err(); err != nil {
			return ReconciliationResult{}, err
		}
		if t.WalletID != wager.WalletID(w.ID()) {
			continue
		} // linked foreign rows were checked above
		movement := expectedMovement(t, refs)
		if t.Currency != w.Currency() && counts[t.ID] == 0 {
			add(LedgerCurrencyMismatch, t.ID, "", w.Currency(), t.Currency, "transaction currency differs from wallet currency")
		}
		if string(t.PlayerID) != string(w.PlayerID()) {
			add(TransactionInvalid, t.ID, "", string(w.PlayerID()), string(t.PlayerID), "transaction player differs from wallet owner")
		}
		if !movement.valid {
			add(TransactionInvalid, t.ID, "", "valid transaction semantics", string(t.State)+":"+string(t.Type), "transaction state, amount or reference is incompatible with its operation")
			versionValid = false
		}
		if movement.required {
			if counts[t.ID] == 0 {
				add(TransactionLedgerMissing, t.ID, "", "one financial ledger movement", "none", "processed financial transaction has no ledger movement")
			}
			if t.Type != wager.TypeOpening && movement.valid {
				version++
			}
		}
		if t.Type == wager.TypeOpening {
			openings++
			if openings > 1 {
				add(TransactionInvalid, t.ID, "", "at most one OPENING", strconv.Itoa(openings), "wallet contains multiple opening transactions")
				versionValid = false
			}
		}
	}
	if versionValid {
		r.ExpectedWalletVersion = &version
		if w.Version() != version {
			add(WalletVersionMismatch, "", "", strconv.FormatInt(version, 10), strconv.FormatInt(w.Version(), 10), "wallet version differs from processed balance-changing transactions excluding opening")
		}
	}
	if len(r.Divergences) > 0 {
		r.Status = "DIVERGENT"
	}
	r.StoredBalance = r.WalletBalance
	r.CalculatedBalance = r.LedgerBalance
	r.Consistent = r.Status == "CONSISTENT"
	r.CheckedEntries = r.LedgerEntriesChecked
	if reconstructable {
		if difference, err := w.Balance().Sub(balance); err == nil {
			external := difference.External()
			r.Difference = &external
		}
	}
	r.CheckedAt = time.Now().UTC()
	if err := ctx.Err(); err != nil {
		return ReconciliationResult{}, err
	}
	return r, nil
}
