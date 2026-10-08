package financial

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

var ErrInvalidPagination = errors.New("invalid ledger pagination")

type ReadService struct {
	secured *AuthorizedService
	reader  ports.FinancialReader
}

func NewReadService(secured *AuthorizedService, reader ports.FinancialReader) *ReadService {
	return &ReadService{secured: secured, reader: reader}
}

type LedgerPage struct {
	Wallet     wallet.Wallet
	Entries    []ports.LedgerEntry
	NextCursor string
}
type ledgerCursor struct {
	Version       int       `json:"v"`
	WalletID      wallet.ID `json:"w"`
	WalletVersion int64     `json:"n"`
	EntryID       string    `json:"i"`
}

func (s *ReadService) Ledger(ctx context.Context, p identity.Principal, id wallet.ID, cursor string, limit int) (LedgerPage, error) {
	w, err := s.secured.GetWallet(ctx, p, id)
	if err != nil {
		return LedgerPage{}, err
	}
	if limit < 1 || limit > 100 {
		return LedgerPage{}, ErrInvalidPagination
	}
	var after ports.LedgerPosition
	if cursor != "" {
		if len(cursor) > 2048 {
			return LedgerPage{}, ErrInvalidPagination
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(cursor)
		if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != cursor {
			return LedgerPage{}, ErrInvalidPagination
		}
		var c ledgerCursor
		d := json.NewDecoder(bytes.NewReader(decoded))
		d.DisallowUnknownFields()
		if d.Decode(&c) != nil || c.Version != 1 || c.WalletID != id || c.WalletVersion < 1 || c.EntryID == "" || len(c.EntryID) > 512 {
			return LedgerPage{}, ErrInvalidPagination
		}
		if d.Decode(new(any)) != io.EOF {
			return LedgerPage{}, ErrInvalidPagination
		}
		after = ports.LedgerPosition{WalletVersion: c.WalletVersion, EntryID: c.EntryID}
	}
	entries, err := s.reader.ListLedger(ctx, id, after, limit+1)
	if err != nil {
		return LedgerPage{}, err
	}
	page := LedgerPage{Wallet: w, Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		last := page.Entries[limit-1]
		b, _ := json.Marshal(ledgerCursor{Version: 1, WalletID: id, WalletVersion: last.WalletVersion, EntryID: last.ID})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return page, nil
}

type TransactionDetail struct {
	Transaction wager.Transaction
	Result      *WagerResult
}

func (s *ReadService) Transaction(ctx context.Context, p identity.Principal, id wager.TransactionID) (TransactionDetail, error) {
	internal := s.secured.auth.RequireInternal(p) == nil
	if !internal {
		if err := s.secured.auth.RequireProvider(p); err != nil {
			return TransactionDetail{}, err
		}
	}
	snapshot, err := s.reader.GetTransactionSnapshot(ctx, id)
	if err != nil {
		return TransactionDetail{}, err
	}
	if !internal {
		if err := s.secured.auth.AuthorizeProvider(p, string(snapshot.Transaction.ProviderID())); err != nil {
			return TransactionDetail{}, err
		}
	}
	detail := TransactionDetail{Transaction: snapshot.Transaction}
	if len(snapshot.SavedResult) > 0 {
		var result WagerResult
		if err := json.Unmarshal(snapshot.SavedResult, &result); err != nil {
			return TransactionDetail{}, ErrPersistence
		}
		if result.TransactionID != id || result.ProviderID != snapshot.Transaction.ProviderID() || result.State != snapshot.Transaction.State() {
			return TransactionDetail{}, ErrPersistence
		}
		detail.Result = &result
	}
	return detail, nil
}

func (s *ReadService) ExternalTransaction(ctx context.Context, p identity.Principal, provider wager.ProviderID, external wager.ExternalTransactionID) (TransactionDetail, error) {
	tx, err := s.secured.GetByExternalID(ctx, p, provider, external)
	if err != nil {
		return TransactionDetail{}, err
	}
	return s.Transaction(ctx, p, tx.ID())
}
