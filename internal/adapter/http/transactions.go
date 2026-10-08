package httpadapter

import (
	"net/http"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

type wagerRequest struct {
	ProviderID                     string         `json:"providerId,omitempty"`
	ExternalTransactionID          string         `json:"externalTransactionId"`
	PlayerID                       string         `json:"playerId"`
	WalletID                       string         `json:"walletId"`
	Type                           wager.Type     `json:"type"`
	Kind                           wager.Type     `json:"kind,omitempty"`
	GameID                         string         `json:"gameId,omitempty"`
	Money                          money.External `json:"money"`
	RoundID                        string         `json:"roundId"`
	ReferenceExternalTransactionID string         `json:"referenceExternalTransactionId,omitempty"`
}

func (h *FinancialHandler) ProcessWager(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	if err := h.authorizer.RequireProvider(p); err != nil {
		writeApplicationError(w, err)
		return
	}
	keys := r.Header.Values("Idempotency-Key")
	if len(keys) == 0 {
		writeApplicationError(w, financial.ErrIdempotencyKeyRequired)
		return
	}
	if len(keys) != 1 || !validIdentifier(keys[0]) {
		WriteError(w, 400, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key must be one nonempty value up to 256 bytes")
		return
	}
	var dto wagerRequest
	if !decodeJSON(w, r, &dto) {
		return
	}
	if dto.ProviderID != "" {
		if err := h.authorizer.AuthorizeProvider(p, dto.ProviderID); err != nil {
			writeApplicationError(w, err)
			return
		}
	}
	if dto.Kind != "" {
		if dto.Type != "" && dto.Type != dto.Kind {
			writeApplicationError(w, financial.ErrInvalidInput)
			return
		}
		dto.Type = dto.Kind
		if !requireIDs(w, dto.GameID) {
			return
		}
	}
	if dto.GameID != "" && !requireIDs(w, dto.GameID) {
		return
	}
	if !requireIDs(w, dto.ExternalTransactionID, dto.PlayerID, dto.WalletID, dto.RoundID) {
		return
	}
	if dto.ReferenceExternalTransactionID != "" && !requireIDs(w, dto.ReferenceExternalTransactionID) {
		return
	}
	if !dto.Type.IsExternal() {
		writeApplicationError(w, financial.ErrInvalidOperationType)
		return
	}
	amount, err := parseHTTPMoney(dto.Money)
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	result, err := h.commands.ProcessHTTPWager(r.Context(), p, financial.HTTPWagerInput{WagerInput: financial.WagerInput{ProviderID: wager.ProviderID(p.ProviderID), ExternalTransactionID: wager.ExternalTransactionID(dto.ExternalTransactionID), PlayerID: wager.PlayerID(dto.PlayerID), WalletID: wallet.ID(dto.WalletID), Type: dto.Type, Amount: amount, RoundID: wager.RoundID(dto.RoundID), GameID: dto.GameID, ReferenceExternalTransactionID: wager.ExternalTransactionID(dto.ReferenceExternalTransactionID), CorrelationID: correlation(r)}, IdempotencyKey: keys[0]})
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	status := 201
	if result.IdempotentReplay {
		status = 200
	}
	switch result.State {
	case wager.StatePendingReference:
		status = 202
	case wager.StateRejected, wager.StateFailed:
		status = 422
	}
	setRequestResult(r, string(result.TransactionID), string(result.WalletID), result.IdempotentReplay)
	w.Header().Set("Location", "/wagering/transactions/"+string(result.TransactionID))
	WriteJSON(w, status, struct {
		financial.WagerResult
		Status  wager.State    `json:"status"`
		Balance money.External `json:"balance"`
	}{result, result.State, result.ObservedBalance})
}

type transactionResponse struct {
	GameID                         string                      `json:"gameId,omitempty"`
	TransactionID                  wager.TransactionID         `json:"transactionId"`
	ProviderID                     wager.ProviderID            `json:"providerId,omitempty"`
	ExternalTransactionID          wager.ExternalTransactionID `json:"externalTransactionId,omitempty"`
	PlayerID                       wager.PlayerID              `json:"playerId"`
	WalletID                       wager.WalletID              `json:"walletId"`
	Type                           wager.Type                  `json:"type"`
	State                          wager.State                 `json:"state"`
	Money                          money.External              `json:"money"`
	RoundID                        wager.RoundID               `json:"roundId,omitempty"`
	ReferenceExternalTransactionID wager.ExternalTransactionID `json:"referenceExternalTransactionId,omitempty"`
	FailureCode                    wager.FailureCode           `json:"failureCode,omitempty"`
	CreatedAt                      time.Time                   `json:"createdAt"`
	UpdatedAt                      time.Time                   `json:"updatedAt"`
	Result                         *financial.WagerResult      `json:"result,omitempty"`
}

func writeTransaction(w http.ResponseWriter, r *http.Request, detail financial.TransactionDetail) {
	tx := detail.Transaction
	setRequestResult(r, string(tx.ID()), string(tx.WalletID()), false, string(tx.ProviderID()))
	WriteJSON(w, 200, transactionResponse{GameID: tx.GameID(), TransactionID: tx.ID(), ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), PlayerID: tx.PlayerID(), WalletID: tx.WalletID(), Type: tx.Type(), State: tx.State(), Money: tx.Amount().External(), RoundID: tx.RoundID(), ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID(), FailureCode: tx.FailureCode(), CreatedAt: tx.CreatedAt().UTC(), UpdatedAt: tx.UpdatedAt().UTC(), Result: detail.Result})
}

func (h *FinancialHandler) GetTransaction(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizer.RequireTransactionRead(principal(r)); err != nil {
		writeApplicationError(w, err)
		return
	}
	id := r.PathValue("transactionId")
	if !requireIDs(w, id) {
		return
	}
	detail, err := h.reads.Transaction(r.Context(), principal(r), wager.TransactionID(id))
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	writeTransaction(w, r, detail)
}
func (h *FinancialHandler) GetExternalTransaction(w http.ResponseWriter, r *http.Request) {
	p := principal(r)
	provider, external := r.PathValue("providerId"), r.PathValue("externalTransactionId")
	if err := h.authorizer.AuthorizeProvider(p, provider); err != nil {
		writeApplicationError(w, err)
		return
	}
	if !requireIDs(w, provider, external) {
		return
	}
	detail, err := h.reads.ExternalTransaction(r.Context(), p, wager.ProviderID(provider), wager.ExternalTransactionID(external))
	if err != nil {
		writeApplicationError(w, err)
		return
	}
	writeTransaction(w, r, detail)
}
