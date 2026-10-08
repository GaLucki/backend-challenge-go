package financial

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

func auditFixture() ports.ReconciliationSnapshot {
	m, _ := money.New(8000, "BRL")
	w, _ := wallet.Rehydrate("wallet", "player", "BRL", m, 2)
	return ports.ReconciliationSnapshot{Wallet: w, Ledger: []ports.LedgerEntry{
		{ID: "opening:ledger", WalletID: "wallet", TransactionID: "opening", Direction: ports.Credit, AmountCents: 10000, BalanceAfterCents: 10000, WalletVersion: 1},
		{ID: "bet:ledger", WalletID: "wallet", TransactionID: "bet", Direction: ports.Debit, AmountCents: 2000, BalanceBeforeCents: 10000, BalanceAfterCents: 8000, WalletVersion: 2},
	}, Transactions: []ports.AuditTransaction{
		{ID: "opening", PlayerID: "player", WalletID: "wallet", Currency: "BRL", Type: wager.TypeOpening, AmountCents: 10000, State: wager.StateProcessed},
		{ID: "bet", ProviderID: "provider", ExternalID: "bet", PlayerID: "player", WalletID: "wallet", Currency: "BRL", Type: wager.TypeBet, AmountCents: 2000, RoundID: "round", State: wager.StateProcessed},
	}}
}

func hasDivergence(r ReconciliationResult, code DivergenceCode) bool {
	for _, d := range r.Divergences {
		if d.Code == code {
			return true
		}
	}
	return false
}

func TestAuditSnapshotCorruptEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*ports.ReconciliationSnapshot)
		codes  []DivergenceCode
	}{
		{"balance", func(s *ports.ReconciliationSnapshot) {
			m, _ := money.New(7900, "BRL")
			s.Wallet, _ = wallet.Rehydrate("wallet", "player", "BRL", m, 2)
		}, []DivergenceCode{BalanceMismatch}},
		{"first before", func(s *ports.ReconciliationSnapshot) { s.Ledger[0].BalanceBeforeCents = 1 }, []DivergenceCode{LedgerChainBroken}},
		{"chain", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].BalanceBeforeCents = 9900 }, []DivergenceCode{LedgerChainBroken}},
		{"after", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].BalanceAfterCents = 7900 }, []DivergenceCode{LedgerChainBroken, BalanceMismatch}},
		{"amount", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].AmountCents = 1900 }, []DivergenceCode{LedgerAmountMismatch}},
		{"direction", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].Direction = ports.Credit }, []DivergenceCode{LedgerDirectionMismatch}},
		{"currency", func(s *ports.ReconciliationSnapshot) { s.Transactions[1].Currency = "USD" }, []DivergenceCode{LedgerCurrencyMismatch}},
		{"foreign wallet", func(s *ports.ReconciliationSnapshot) { s.Transactions[1].WalletID = "foreign" }, []DivergenceCode{TransactionWalletMismatch}},
		{"missing tx", func(s *ports.ReconciliationSnapshot) { s.Transactions = s.Transactions[:1] }, []DivergenceCode{LedgerTransactionNotFound}},
		{"missing ledger", func(s *ports.ReconciliationSnapshot) { s.Ledger = s.Ledger[:1] }, []DivergenceCode{TransactionLedgerMissing}},
		{"loss ledger", func(s *ports.ReconciliationSnapshot) {
			s.Transactions[1].Type = wager.TypeLoss
			s.Transactions[1].AmountCents = 0
		}, []DivergenceCode{UnexpectedLedgerEntry}},
		{"rejected ledger", func(s *ports.ReconciliationSnapshot) { s.Transactions[1].State = wager.StateRejected }, []DivergenceCode{UnexpectedLedgerEntry}},
		{"failed ledger", func(s *ports.ReconciliationSnapshot) { s.Transactions[1].State = wager.StateFailed }, []DivergenceCode{UnexpectedLedgerEntry}},
		{"pending ledger", func(s *ports.ReconciliationSnapshot) { s.Transactions[1].State = wager.StatePendingReference }, []DivergenceCode{UnexpectedLedgerEntry}},
		{"duplicate", func(s *ports.ReconciliationSnapshot) {
			e := s.Ledger[1]
			e.ID = "duplicate"
			s.Ledger = append(s.Ledger, e)
		}, []DivergenceCode{DuplicateLedgerEntry}},
		{"version", func(s *ports.ReconciliationSnapshot) {
			s.Wallet, _ = wallet.Rehydrate("wallet", "player", "BRL", s.Wallet.Balance(), 3)
		}, []DivergenceCode{WalletVersionMismatch}},
		{"ledger version", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].WalletVersion = 8 }, []DivergenceCode{WalletVersionMismatch}},
		{"negative", func(s *ports.ReconciliationSnapshot) {
			s.Ledger[1].AmountCents = 11000
			s.Ledger[1].BalanceAfterCents = -1000
		}, []DivergenceCode{NegativeReconstructedBalance}},
		{"zero movement", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].AmountCents = 0 }, []DivergenceCode{LedgerAmountMismatch}},
		{"invalid direction", func(s *ports.ReconciliationSnapshot) { s.Ledger[1].Direction = "OTHER" }, []DivergenceCode{LedgerDirectionMismatch}},
		{"overflow", func(s *ports.ReconciliationSnapshot) {
			s.Ledger[0].AmountCents = math.MaxInt64
			s.Ledger[0].BalanceAfterCents = math.MaxInt64
			s.Ledger[1].BalanceBeforeCents = math.MaxInt64
			s.Ledger[1].Direction = ports.Credit
		}, []DivergenceCode{LedgerArithmeticOverflow}},
		{"invalid reversal", func(s *ports.ReconciliationSnapshot) {
			s.Transactions[1].Type = wager.TypeRollback
			s.Transactions[1].ReferenceExternalID = "missing"
		}, []DivergenceCode{TransactionInvalid, UnexpectedLedgerEntry}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := auditFixture()
			tc.mutate(&s)
			r, err := auditSnapshot(context.Background(), s)
			if err != nil || r.Status != "DIVERGENT" {
				t.Fatal(r, err)
			}
			for _, code := range tc.codes {
				if !hasDivergence(r, code) {
					t.Fatal("missing", code, r.Divergences)
				}
			}
			if tc.name == "overflow" && r.LedgerBalance != nil {
				t.Fatal("overflow returned fabricated balance")
			}
			if tc.name == "invalid reversal" && r.ExpectedWalletVersion != nil {
				t.Fatal("invalid semantics returned fabricated version")
			}
		})
	}
}

func TestAuditSnapshotZeroOpeningOrderingAndTerminalStates(t *testing.T) {
	s := auditFixture()
	s.Ledger[0], s.Ledger[1] = s.Ledger[1], s.Ledger[0]
	for _, state := range []wager.State{wager.StatePending, wager.StatePendingReference, wager.StateRejected, wager.StateFailed} {
		tx := s.Transactions[1]
		tx.ID = wager.TransactionID(state)
		tx.ExternalID = wager.ExternalTransactionID(state)
		tx.State = state
		s.Transactions = append(s.Transactions, tx)
	}
	loss := s.Transactions[1]
	loss.ID = "loss"
	loss.Type = wager.TypeLoss
	loss.AmountCents = 0
	s.Transactions = append(s.Transactions, loss)
	before := append([]ports.LedgerEntry(nil), s.Ledger...)
	r, err := auditSnapshot(context.Background(), s)
	if err != nil || r.Status != "CONSISTENT" || *r.ExpectedWalletVersion != 2 || r.LedgerBalance.Amount != "80.00" || !reflect.DeepEqual(s.Ledger, before) {
		t.Fatal(r, err)
	}
	m, _ := money.Zero("BRL")
	s.Wallet, _ = wallet.New("zero", "player", "BRL", m)
	s.Ledger = nil
	s.Transactions = nil
	r, err = auditSnapshot(context.Background(), s)
	if err != nil || r.Status != "CONSISTENT" || *r.ExpectedWalletVersion != 1 || r.LedgerBalance.Amount != "0.00" || r.Divergences == nil {
		t.Fatal(r, err)
	}
}

type auditReaderSpy struct {
	snapshot ports.ReconciliationSnapshot
	err      error
	calls    int
}

func (s *auditReaderSpy) ReadReconciliation(context.Context, wallet.ID) (ports.ReconciliationSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
}

func TestReconciliationApplicationAuthorizationAndTechnicalFailures(t *testing.T) {
	reader := &auditReaderSpy{snapshot: auditFixture()}
	s := NewReconciliationService(reader, identity.NewAuthorizer())
	internal := identity.Principal{Subject: "subject", ClientID: "internal", Internal: true, Roles: []string{identity.RoleInternal}}
	for _, p := range []identity.Principal{{}, {Subject: "s", ClientID: "provider", ProviderID: "provider", Roles: []string{identity.RoleProvider}}} {
		if _, err := s.Reconcile(context.Background(), p, "wallet"); err == nil {
			t.Fatal("unauthorized audit")
		}
	}
	if reader.calls != 0 {
		t.Fatal("unauthorized persistence read")
	}
	for _, tc := range []struct{ err, want error }{{ports.ErrNotFound, ErrWalletNotFound}, {errors.New("SQL password secret"), ErrPersistence}, {context.Canceled, context.Canceled}, {context.DeadlineExceeded, context.DeadlineExceeded}} {
		reader.err = tc.err
		r, err := s.Reconcile(context.Background(), internal, "wallet")
		if !errors.Is(err, tc.want) || r.Status != "" {
			t.Fatal(r, err)
		}
	}
	reader.err = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, err := s.Reconcile(ctx, internal, "wallet"); !errors.Is(err, context.Canceled) || r.Status != "" {
		t.Fatal("canceled audit returned a report")
	}
}
