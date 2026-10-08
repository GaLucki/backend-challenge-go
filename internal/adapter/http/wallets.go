package httpadapter

import (
	"net/http"
	"strconv"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

type createWalletRequest struct {
	PlayerID       string          `json:"playerId"`
	Currency       string          `json:"currency"`
	InitialBalance *money.External `json:"initialBalance,omitempty"`
}
type walletResponse struct {
	WalletID  wallet.ID       `json:"walletId"`
	PlayerID  wallet.PlayerID `json:"playerId"`
	Currency  string          `json:"currency"`
	Balance   money.External  `json:"balance"`
	Version   int64           `json:"version"`
	CreatedAt time.Time       `json:"createdAt"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

func (h *FinancialHandler) CreateWallet(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := h.authorizer.RequireInternal(p); err != nil {
		writeApplicationError(w, err)
		return
	}
	var dto createWalletRequest
	if !decodeJSON(w, r, &dto) || !requireIDs(w, dto.PlayerID) {
		return
	}
	if dto.Currency == "" && dto.InitialBalance != nil {
		dto.Currency = dto.InitialBalance.Currency
	}
	initial, err := money.Zero(dto.Currency)
	if err != nil {
		writeApplicationError(w, financial.ErrCurrencyMismatch)
		return
	}
	if dto.InitialBalance != nil {
		initial, err = parseHTTPMoney(*dto.InitialBalance)
		if err != nil {
			writeApplicationError(w, err)
			return
		}
	}
	result, err := h.commands.CreateWallet(r.Context(), p, financial.CreateWalletInput{PlayerID: wallet.PlayerID(dto.PlayerID), Currency: dto.Currency, InitialBalance: initial, CorrelationID: correlation(r)})
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	setRequestResult(r, "", string(result.WalletID), false)
	w.Header().Set("Location", "/wallets/"+string(result.WalletID))
	WriteJSON(w, 201, struct {
		financial.CreateWalletResult
		ID      wallet.ID `json:"id"`
		Version int64     `json:"version"`
	}{result, result.WalletID, result.WalletVersion})
}

func (h *FinancialHandler) GetWallet(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := h.authorizer.RequireInternal(p); err != nil {
		writeApplicationError(w, err)
		return
	}
	id := r.PathValue("walletId")
	if !requireIDs(w, id) {
		return
	}
	result, err := h.commands.GetWallet(r.Context(), p, wallet.ID(id))
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	setRequestResult(r, "", string(result.ID()), false)
	WriteJSON(w, 200, walletResponse{WalletID: result.ID(), PlayerID: result.PlayerID(), Currency: result.Currency(), Balance: result.Balance().External(), Version: result.Version(), CreatedAt: result.CreatedAt(), UpdatedAt: result.UpdatedAt()})
}

type ledgerItemResponse struct {
	LedgerEntryID string                `json:"ledgerEntryId"`
	WalletID      wallet.ID             `json:"walletId"`
	TransactionID string                `json:"transactionId"`
	Direction     ports.LedgerDirection `json:"direction"`
	Amount        money.External        `json:"amount"`
	BalanceBefore money.External        `json:"balanceBefore"`
	BalanceAfter  money.External        `json:"balanceAfter"`
	WalletVersion int64                 `json:"walletVersion"`
	OccurredAt    time.Time             `json:"occurredAt"`
}
type ledgerResponse struct {
	Items      []ledgerItemResponse `json:"items"`
	NextCursor string               `json:"nextCursor,omitempty"`
}

func (h *FinancialHandler) Ledger(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := h.authorizer.RequireInternal(p); err != nil {
		writeApplicationError(w, err)
		return
	}
	id := r.PathValue("walletId")
	if !requireIDs(w, id) {
		return
	}
	// ParseQuery must not silently drop malformed query escapes/semicolons.
	q, err := parseLedgerQuery(r)
	if err != nil {
		writeApplicationError(w, financial.ErrInvalidPagination)
		return
	}
	limit := 50
	if values, ok := q["limit"]; ok {
		if len(values) != 1 {
			writeApplicationError(w, financial.ErrInvalidPagination)
			return
		}
		limit, err = strconv.Atoi(values[0])
		if err != nil || limit < 1 || limit > 100 {
			writeApplicationError(w, financial.ErrInvalidPagination)
			return
		}
	}
	cursor := ""
	if values, ok := q["cursor"]; ok {
		if len(values) != 1 || values[0] == "" {
			writeApplicationError(w, financial.ErrInvalidPagination)
			return
		}
		cursor = values[0]
	}
	page, err := h.reads.Ledger(r.Context(), p, wallet.ID(id), cursor, limit)
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	response := ledgerResponse{Items: make([]ledgerItemResponse, 0, len(page.Entries)), NextCursor: page.NextCursor}
	for _, entry := range page.Entries {
		amount, _ := money.New(entry.AmountCents, page.Wallet.Currency())
		before, _ := money.New(entry.BalanceBeforeCents, page.Wallet.Currency())
		after, _ := money.New(entry.BalanceAfterCents, page.Wallet.Currency())
		response.Items = append(response.Items, ledgerItemResponse{LedgerEntryID: entry.ID, WalletID: entry.WalletID, TransactionID: string(entry.TransactionID), Direction: entry.Direction, Amount: amount.External(), BalanceBefore: before.External(), BalanceAfter: after.External(), WalletVersion: entry.WalletVersion, OccurredAt: entry.CreatedAt.UTC()})
	}
	setRequestResult(r, "", id, false)
	WriteJSON(w, 200, response)
}

func (h *FinancialHandler) Reconciliation(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizer.RequireInternal(principal(r)); err != nil {
		writeApplicationError(w, err)
		return
	}
	if !requireIDs(w, r.PathValue("walletId")) {
		return
	}
	id := r.PathValue("walletId")
	setRequestResult(r, "", id, false)
	result, err := h.reconciliation.Reconcile(r.Context(), principal(r), wallet.ID(id))
	if err != nil {
		if info := requestInfoFromContext(r.Context()); info != nil {
			info.ReconciliationStatus = "ERROR"
		}
		writeApplicationError(w, err)
		return
	}
	if info := requestInfoFromContext(r.Context()); info != nil {
		info.ReconciliationStatus = result.Status
		info.LedgerEntriesChecked = result.LedgerEntriesChecked
		info.TransactionsChecked = result.TransactionsChecked
		info.DivergenceCount = len(result.Divergences)
	}
	WriteJSON(w, http.StatusOK, result)
}
