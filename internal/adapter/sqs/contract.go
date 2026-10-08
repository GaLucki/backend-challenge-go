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
	Data                           *CommandData   `json:"data,omitempty"`
	GameID                         string         `json:"gameId,omitempty"`
	IdempotencyKey                 string         `json:"idempotencyKey,omitempty"`
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

// CommandData matches the evaluator's original WagerTransactionRequested
// envelope. The existing flat schemaVersion=1 remains accepted for recovery.
type CommandData struct {
	ProviderID                     string         `json:"providerId"`
	ExternalTransactionID          string         `json:"externalTransactionId"`
	IdempotencyKey                 string         `json:"idempotencyKey"`
	PlayerID                       string         `json:"playerId"`
	WalletID                       string         `json:"walletId"`
	RoundID                        string         `json:"roundId"`
	GameID                         string         `json:"gameId"`
	Kind                           wager.Type     `json:"kind"`
	Money                          money.External `json:"money"`
	ReferenceExternalTransactionID string         `json:"referenceExternalTransactionId,omitempty"`
}

func (e Envelope) normalized() (Envelope, error) {
	if e.Data == nil {
		return e, nil
	}
	d := e.Data
	if e.Type != "WagerTransactionRequested" || e.SchemaVersion != 0 || e.ProviderID != "" || e.ExternalTransactionID != "" || e.PlayerID != "" || e.WalletID != "" || e.RoundID != "" || e.GameID != "" || e.IdempotencyKey != "" || e.Money != (money.External{}) || e.ReferenceExternalTransactionID != "" {
		return e, ErrInvalidEnvelope
	}
	if strings.TrimSpace(d.GameID) == "" || strings.TrimSpace(d.GameID) != d.GameID || strings.TrimSpace(d.IdempotencyKey) != d.IdempotencyKey || d.IdempotencyKey == "" || len(d.IdempotencyKey) > 256 || strings.ContainsAny(d.IdempotencyKey, "\r\n\t") {
		return e, ErrInvalidEnvelope
	}
	e.Data = nil
	e.SchemaVersion = 1
	e.ProviderID, e.ExternalTransactionID = d.ProviderID, d.ExternalTransactionID
	e.PlayerID, e.WalletID, e.RoundID, e.GameID = d.PlayerID, d.WalletID, d.RoundID, d.GameID
	e.Type, e.Money, e.ReferenceExternalTransactionID = d.Kind, d.Money, d.ReferenceExternalTransactionID
	e.IdempotencyKey = d.IdempotencyKey
	if e.CorrelationID == "" {
		e.CorrelationID = e.MessageID
	}
	if e.CausationID == "" {
		e.CausationID = e.MessageID
	}
	return e, nil
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
	var err error
	e, err = e.normalized()
	if err != nil {
		return e, financial.WagerInput{}, err
	}
	in, err := e.Input()
	return e, in, err
}
func (e Envelope) Input() (financial.WagerInput, error) {
	var err error
	e, err = e.normalized()
	if err != nil {
		return financial.WagerInput{}, err
	}
	if e.SchemaVersion != 1 || e.OccurredAt.IsZero() {
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	if e.GameID != "" && strings.TrimSpace(e.GameID) != e.GameID {
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
	if e.GameID != "" && e.Money.Amount != amount.DecimalString() {
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
		if !amount.IsPositive() || strings.TrimSpace(e.ReferenceExternalTransactionID) == "" {
			return financial.WagerInput{}, ErrInvalidEnvelope
		}
	default:
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	if e.Type != wager.TypeRefund && e.Type != wager.TypeRollback && e.Type != wager.TypeWin && e.ReferenceExternalTransactionID != "" {
		return financial.WagerInput{}, ErrInvalidEnvelope
	}
	return financial.WagerInput{ProviderID: wager.ProviderID(e.ProviderID), ExternalTransactionID: wager.ExternalTransactionID(e.ExternalTransactionID), PlayerID: wager.PlayerID(e.PlayerID), WalletID: wallet.ID(e.WalletID), Type: e.Type, Amount: amount, RoundID: wager.RoundID(e.RoundID), GameID: e.GameID, ReferenceExternalTransactionID: wager.ExternalTransactionID(e.ReferenceExternalTransactionID), CorrelationID: e.CorrelationID}, nil
}

// Hash includes envelope metadata as well as canonical business fields. AWS
// receipt handles, receive counts and AWS-generated MessageId are excluded.
func (e Envelope) Hash() (string, error) {
	var err error
	e, err = e.normalized()
	if err != nil {
		return "", err
	}
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
	var err error
	e, err = e.normalized()
	if err != nil {
		return nil, err
	}
	if _, err := e.Input(); err != nil {
		return nil, err
	}
	body, err := json.Marshal(e)
	if err != nil {
		return nil, ErrInvalidEnvelope
	}
	return &awssqs.SendMessageInput{QueueUrl: aws.String(queueURL), MessageBody: aws.String(string(bytes.TrimSpace(body))), MessageGroupId: aws.String(MessageGroupID(e.WalletID)), MessageDeduplicationId: aws.String(MessageDeduplicationID(e.MessageID))}, nil
}
