package financial

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

// AuthorizedService is the entry point for authenticated external requests.
// Workers retain Service for the broker-authenticated internal channel.
type AuthorizedService struct {
	core    *Service
	auth    *identity.Authorizer
	wagers  ports.WagerTransactionRepository
	wallets ports.WalletRepository
}

func NewAuthorizedService(core *Service, auth *identity.Authorizer, wagers ports.WagerTransactionRepository, wallets ports.WalletRepository) *AuthorizedService {
	return &AuthorizedService{core: core, auth: auth, wagers: wagers, wallets: wallets}
}

func (s *AuthorizedService) ProcessHTTPWager(ctx context.Context, p identity.Principal, in HTTPWagerInput) (WagerResult, error) {
	if err := s.auth.RequireProvider(p); err != nil {
		return WagerResult{}, err
	}
	if in.ProviderID != "" {
		if err := s.auth.AuthorizeProvider(p, string(in.ProviderID)); err != nil {
			return WagerResult{}, err
		}
	}
	in.ProviderID = wager.ProviderID(p.ProviderID)
	if strings.TrimSpace(in.IdempotencyKey) == "" {
		return WagerResult{}, ErrIdempotencyKeyRequired
	}
	// Structured encoding avoids separator ambiguity; the namespace separates
	// HTTP identities from historical/internal raw keys without a schema change.
	in.IdempotencyKey = ScopedIdempotencyKey(p.ProviderID, in.IdempotencyKey)
	return s.core.ProcessHTTPWager(ctx, in)
}

// ScopedIdempotencyKey is shared by authenticated HTTP and broker commands.
// The broker channel must be restricted to trusted internal producers.
func ScopedIdempotencyKey(provider, key string) string {
	pair, _ := json.Marshal([2]string{provider, key})
	hash := sha256.Sum256(pair)
	return "oidc:v1:" + hex.EncodeToString(hash[:])
}

func (s *AuthorizedService) GetTransaction(ctx context.Context, p identity.Principal, id wager.TransactionID) (wager.Transaction, error) {
	internal := s.auth.RequireInternal(p) == nil
	if !internal {
		if err := s.auth.RequireProvider(p); err != nil {
			return wager.Transaction{}, err
		}
	}
	tx, err := s.wagers.Get(ctx, id)
	if err != nil {
		return wager.Transaction{}, err
	}
	// OPENING has no provider identity and is readable only internally.
	if !internal {
		if err := s.auth.AuthorizeProvider(p, string(tx.ProviderID())); err != nil {
			return wager.Transaction{}, err
		}
	}
	return tx, nil
}

func (s *AuthorizedService) GetByExternalID(ctx context.Context, p identity.Principal, provider wager.ProviderID, external wager.ExternalTransactionID) (wager.Transaction, error) {
	if err := s.auth.AuthorizeProvider(p, string(provider)); err != nil {
		return wager.Transaction{}, err
	}
	return s.wagers.GetByExternalID(ctx, provider, external)
}

func (s *AuthorizedService) CreateWallet(ctx context.Context, p identity.Principal, in CreateWalletInput) (CreateWalletResult, error) {
	if err := s.auth.RequireInternal(p); err != nil {
		return CreateWalletResult{}, err
	}
	return s.core.CreateWallet(ctx, in)
}

// Wallets are player-scoped, not provider-owned. Reads stay internal-only until
// a separate player/provider wallet access policy is defined in a later phase.
func (s *AuthorizedService) GetWallet(ctx context.Context, p identity.Principal, id wallet.ID) (wallet.Wallet, error) {
	if err := s.auth.RequireInternal(p); err != nil {
		return wallet.Wallet{}, err
	}
	return s.wallets.Get(ctx, id)
}
