package financial

import (
	"context"
	"errors"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

var (
	ErrDeliveryIntegrity       = errors.New("delivery payload integrity conflict")
	ErrExternalPayloadConflict = errors.New("external transaction payload conflict")
)

type DeliveryInput struct {
	ConsumerName string
	MessageID    string
	PayloadHash  string
	Wager        WagerInput
}
type DeliveryResult struct {
	Outcome   string
	Financial WagerResult
}

// ProcessDelivery owns Inbox and the existing financial core in one UoW.
// It returns only after COMMIT; an adapter may acknowledge the delivery afterward.
func (s *Service) ProcessDelivery(ctx context.Context, in DeliveryInput) (DeliveryResult, error) {
	if strings.TrimSpace(in.ConsumerName) == "" || strings.TrimSpace(in.MessageID) == "" || len(in.PayloadHash) != 64 {
		return DeliveryResult{}, ErrInvalidInput
	}
	if err := validateWager(in.Wager); err != nil {
		return DeliveryResult{}, err
	}
	var result DeliveryResult
	err := s.uow.WithinTransaction(ctx, func(r ports.Repositories) error {
		_, err := r.Inbox.Claim(ctx, ports.InboxMessage{ConsumerName: in.ConsumerName, MessageID: in.MessageID, PayloadHash: in.PayloadHash, ReceivedAt: s.now()})
		if err != nil {
			return err
		}
		record, err := r.Inbox.GetForUpdate(ctx, in.ConsumerName, in.MessageID)
		if err != nil {
			return err
		}
		if record.PayloadHash != in.PayloadHash {
			return ErrDeliveryIntegrity
		}
		if record.CompletedAt != nil {
			result.Outcome = "duplicate"
			return nil
		}
		financial, err := s.process(ctx, r, in.Wager)
		if errors.Is(err, ErrDuplicateExternalTransaction) {
			// A prior HTTP/SQS operation is accepted only if its financial payload matches.
			// No current wallet balance is used to reconstruct a financial response.
			prior, readErr := r.Wagers.GetByExternalID(ctx, in.Wager.ProviderID, in.Wager.ExternalTransactionID)
			if readErr != nil {
				return readErr
			}
			if CanonicalPayloadHash(wagerInput(prior)) != CanonicalPayloadHash(in.Wager) {
				return ErrExternalPayloadConflict
			}
			if prior.State() == wager.StatePending {
				return ErrPersistence
			}
			result.Outcome = "external_duplicate"
			result.Financial.TransactionID = prior.ID()
			result.Financial.State = prior.State()
		} else if err != nil {
			return err
		} else {
			result.Financial = financial
			result.Outcome = string(financial.State)
		}
		return r.Inbox.Complete(ctx, in.ConsumerName, in.MessageID, s.now())
	})
	if err != nil {
		if errors.Is(err, ErrDeliveryIntegrity) {
			return DeliveryResult{}, ErrDeliveryIntegrity
		}
		if errors.Is(err, ErrExternalPayloadConflict) {
			return DeliveryResult{}, ErrExternalPayloadConflict
		}
		return DeliveryResult{}, applicationError(err)
	}
	return result, nil
}
func wagerInput(tx wager.Transaction) WagerInput {
	return WagerInput{ProviderID: tx.ProviderID(), ExternalTransactionID: tx.ExternalTransactionID(), PlayerID: tx.PlayerID(), WalletID: wallet.ID(tx.WalletID()), Type: tx.Type(), Amount: tx.Amount(), RoundID: tx.RoundID(), ReferenceExternalTransactionID: tx.ReferenceExternalTransactionID()}
}
