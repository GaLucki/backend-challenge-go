package financial

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
)

// CanonicalPayloadHash uses a versioned typed representation, never raw JSON.
// Identifiers are exact, case-sensitive values; no implicit trimming is applied.
// Money is canonical integer cents and validated uppercase currency.
func CanonicalPayloadHash(in WagerInput) string {
	if in.GameID != "" || (in.Type == "WIN" && in.ReferenceExternalTransactionID != "") {
		// encoding/json sorts map keys. Version 3 covers all current business
		// fields; legacy v1/v2 records retain their original hash and replay.
		payload := map[string]any{"version": 3, "providerId": in.ProviderID,
			"externalTransactionId": in.ExternalTransactionID, "playerId": in.PlayerID,
			"walletId": in.WalletID, "type": in.Type, "amountCents": in.Amount.Cents(),
			"currency": in.Amount.Currency(), "roundId": in.RoundID, "gameId": in.GameID,
			"referenceExternalTransactionId": in.ReferenceExternalTransactionID}
		encoded, _ := json.Marshal(payload)
		hash := sha256.Sum256(encoded)
		return hex.EncodeToString(hash[:])
	}
	payload := struct {
		Version                        int    `json:"version"`
		ProviderID                     string `json:"providerId"`
		ExternalTransactionID          string `json:"externalTransactionId"`
		PlayerID                       string `json:"playerId"`
		WalletID                       string `json:"walletId"`
		Type                           string `json:"type"`
		AmountCents                    int64  `json:"amountCents"`
		Currency                       string `json:"currency"`
		RoundID                        string `json:"roundId"`
		ReferenceExternalTransactionID string `json:"referenceExternalTransactionId,omitempty"`
	}{1, string(in.ProviderID), string(in.ExternalTransactionID), string(in.PlayerID), string(in.WalletID), string(in.Type), in.Amount.Cents(), in.Amount.Currency(), string(in.RoundID), string(in.ReferenceExternalTransactionID)}
	// Preserve byte-for-byte phase 4 hashes for operations without references.
	if isReversal(in.Type) {
		payload.Version = 2
	}
	encoded, _ := json.Marshal(payload) // Only scalar primitives; encoding cannot fail.
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:])
}
