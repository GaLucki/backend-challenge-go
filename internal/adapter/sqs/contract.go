// Package sqs implements the incoming SQS transport; it does not publish the Outbox.
package sqs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
)

var ErrInvalidEnvelope = errors.New("invalid wagering envelope")
var ErrInvalidGroup = errors.New("invalid wallet message group")

type Envelope struct {
	SchemaVersion                  int            `json:"schemaVersion"`
	MessageID                      string         `json:"messageId"`
	ProviderID                     string         `json:"providerId"`
	ExternalTransactionID          string         `json:"externalTransactionId"`
	PlayerID                       string         `json:"playerId"`
	WalletID                       string         `json:"walletId"`
	Type                           wager.Type     `json:"type"`
	Money                          money.External `json:"money"`
	RoundID                        string         `json:"roundId"`
	ReferenceExternalTransactionID string         `json:"referenceExternalTransactionId,omitempty"`
	CorrelationID                  string         `json:"correlationId"`
	CausationID                    string         `json:"causationId"`
	OccurredAt                     time.Time      `json:"occurredAt"`
}

func Parse(body string) (Envelope, financial.WagerInput, error) {
	var e Envelope
	d := json.NewDecoder(strings.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&e); err != nil {
		return e, financial.WagerInput{}, ErrInvalidEnvelope
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return e, financial.WagerInput{}, ErrInvalidEnvelope
	}
	in, err := e.Input()
	return e, in, err
}
func (e Envelope) Input() (financial.WagerInput, error) {
	if e.SchemaVersion != 1 || e.OccurredAt.IsZero() {
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	for _, field := range []string{e.MessageID, e.ProviderID, e.ExternalTransactionID, e.PlayerID, e.WalletID, e.RoundID, e.CorrelationID, e.CausationID} {
		if strings.TrimSpace(field) == "" {
			return financial.WagerInput{}, ErrInvalidEnvelope
		}
	}
	amount, err := money.ParseDecimal(e.Money.Amount, e.Money.Currency)
	if err != nil {
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	switch e.Type {
	case wager.TypeBet, wager.TypeWin:
		if !amount.IsPositive() {
			return financial.WagerInput{}, ErrInvalidEnvelope
		}
	case wager.TypeLoss:
		if !amount.IsZero() {
			return financial.WagerInput{}, ErrInvalidEnvelope
		}
	case wager.TypeRefund, wager.TypeRollback:
		if strings.TrimSpace(e.ReferenceExternalTransactionID) == "" {
			return financial.WagerInput{}, ErrInvalidEnvelope
		}
	default:
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	if e.Type != wager.TypeRefund && e.Type != wager.TypeRollback && e.ReferenceExternalTransactionID != "" {
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	return financial.WagerInput{ProviderID: wager.ProviderID(e.ProviderID), ExternalTransactionID: wager.ExternalTransactionID(e.ExternalTransactionID), PlayerID: wager.PlayerID(e.PlayerID), WalletID: wallet.ID(e.WalletID), Type: e.Type, Amount: amount, RoundID: wager.RoundID(e.RoundID), ReferenceExternalTransactionID: wager.ExternalTransactionID(e.ReferenceExternalTransactionID), CorrelationID: e.CorrelationID}, nil
}

// Hash includes envelope metadata as well as canonical business fields. AWS
// receipt handles, receive counts and AWS-generated MessageId are excluded.
func (e Envelope) Hash() (string, error) {
	in, err := e.Input()
	if err != nil {
		return "", err
	}
	e.Money = in.Amount.External()
	e.OccurredAt = e.OccurredAt.UTC()
	encoded, err := json.Marshal(e)
	if err != nil {
		return "", ErrInvalidEnvelope
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
func identity(prefix, id string) string {
	sum := sha256.Sum256([]byte(id))
	return prefix + hex.EncodeToString(sum[:])
}
func MessageGroupID(walletID string) string          { return identity("wallet:", walletID) }
func MessageDeduplicationID(messageID string) string { return identity("delivery:", messageID) }

// BuildSendInput implements the producer contract for incoming wagering messages.
// This helper is independent of Transactional Outbox publishing.
func BuildSendInput(queueURL string, e Envelope) (*awssqs.SendMessageInput, error) {
	if _, err := e.Input(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	return &awssqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(bytes.TrimSpace(body))), MessageGroupId: aws.String(MessageGroupID(e.WalletID)), MessageDeduplicationId: aws.String(MessageDeduplicationID(e.MessageID))}, nil
}
