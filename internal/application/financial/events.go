package financial

import (
	"context"
	"encoding/json"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

const (
	EventWagerProcessed        = "WagerTransactionProcessed"
	EventWagerRejected         = "WagerTransactionRejected"
	EventWalletBalanceChanged  = "WalletBalanceChanged"
	EventWagerPendingReference = "WagerTransactionPendingReference"
)

type EventEnvelope struct {
	EventID       string    `json:"eventId"`
	EventType     string    `json:"eventType"`
	AggregateID   string    `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId"`
	OccurredAt    time.Time `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          any       `json:"data"`
}
type TransactionEventData struct {
	TransactionID                  wager.TransactionID         `json:"transactionId"`
	WalletID                       wallet.ID                   `json:"walletId"`
	ProviderID                     wager.ProviderID            `json:"providerId,omitempty"`
	ExternalTransactionID          wager.ExternalTransactionID `json:"externalTransactionId,omitempty"`
	PlayerID                       wager.PlayerID              `json:"playerId"`
	RoundID                        wager.RoundID               `json:"roundId,omitempty"`
	Type                           wager.Type                  `json:"type"`
	State                          wager.State                 `json:"state"`
	FailureCode                    wager.FailureCode           `json:"failureCode,omitempty"`
	Money                          money.External              `json:"money"`
	ReferenceExternalTransactionID wager.ExternalTransactionID `json:"referenceExternalTransactionId,omitempty"`
}
type BalanceEventData struct {
	WalletID      wallet.ID             `json:"walletId"`
	TransactionID wager.TransactionID   `json:"transactionId"`
	Direction     ports.LedgerDirection `json:"direction"`
	Money         money.External        `json:"money"`
	BalanceBefore money.External        `json:"balanceBefore"`
	BalanceAfter  money.External        `json:"balanceAfter"`
	WalletVersion int64                 `json:"walletVersion"`
}

func writeEvents(ctx context.Context, r ports.Repositories, tx wager.Transaction, w wallet.Wallet, before money.Money, direction ports.LedgerDirection, correlation string, changed bool) error {
	eventType := EventWagerProcessed
	if tx.State() == wager.StateRejected {
		eventType = EventWagerRejected
	} else if tx.State() == wager.StatePendingReference {
		eventType = EventWagerPendingReference
	}
	data := TransactionEventData{TransactionID: tx.ID(), WalletID: w.ID(), ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), PlayerID: tx.PlayerID(), RoundID: tx.RoundID(), Type: tx.Type(), State: tx.State(), FailureCode: tx.FailureCode(), Money: tx.Amount().External(), ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID()}
	if err := storeEvent(ctx, r.Outbox, tx, eventType, string(tx.ID()), correlation, data); err != nil {
		return err
	}
	if !changed {
		return nil
	}
	balance := BalanceEventData{WalletID: w.ID(), TransactionID: tx.ID(), Direction: direction, Money: tx.Amount().External(), BalanceBefore: before.External(), BalanceAfter: w.Balance().External(), WalletVersion: w.Version()}
	return storeEvent(ctx, r.Outbox, tx, EventWalletBalanceChanged, string(w.ID()), correlation, balance)
}
func storeEvent(ctx context.Context, outbox ports.OutboxRepository, tx wager.Transaction, kind, aggregate, correlation string, data any) error {
	id := string(tx.ID()) + ":" + kind
	at := tx.UpdatedAt().UTC()
	payload, err := json.Marshal(EventEnvelope{EventID: id, EventType: kind, AggregateID: aggregate, CorrelationID: correlation, CausationID: string(tx.ID()), OccurredAt: at, Version: 1, Data: data})
	if err != nil {
		return ErrPersistence
	}
	return outbox.Create(ctx, ports.OutboxEvent{EventID: id, AggregateID: aggregate, EventType: kind, Payload: payload, OccurredAt: at, NextAttemptAt: at})
}
