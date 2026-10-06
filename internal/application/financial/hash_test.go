package financial

import (
	"encoding/json"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

func TestCanonicalHashEquivalentPayloads(t *testing.T) {
	texts := []string{
		`{"amount":"20","currency":"BRL","providerId":"provider","externalTransactionId":"external","playerId":"player","walletId":"wallet","type":"BET","roundId":"round"}`,
		`{ "roundId":"round", "type":"BET", "walletId":"wallet", "playerId":"player", "externalTransactionId":"external", "providerId":"provider", "currency":"BRL", "amount":"20.00" }`,
	}
	var expected string
	for _, text := range texts {
		var decoded struct {
			WagerInput
			Amount   string
			Currency string
		}
		if err := json.Unmarshal([]byte(text), &decoded); err != nil {
			t.Fatal(err)
		}
		amount, err := money.ParseDecimal(decoded.Amount, decoded.Currency)
		if err != nil {
			t.Fatal(err)
		}
		in := decoded.WagerInput
		in.Amount = amount
		if err := validateWager(in); err != nil {
			t.Fatal(err)
		}
		hash := CanonicalPayloadHash(in)
		if expected != "" && hash != expected {
			t.Fatal("semantic equivalence changed hash")
		}
		expected = hash
		in.CorrelationID = "new-correlation"
		if CanonicalPayloadHash(in) != hash {
			t.Fatal("correlation included in hash")
		}
	}
}
func TestCanonicalHashIncludesEveryBusinessField(t *testing.T) {
	base := input(t, "wallet", "BET", 2000).WagerInput
	hash := CanonicalPayloadHash(base)
	for _, modify := range []func(*WagerInput){
		func(in *WagerInput) { in.ProviderID = "other" }, func(in *WagerInput) { in.ExternalTransactionID = "other" },
		func(in *WagerInput) { in.PlayerID = "other" }, func(in *WagerInput) { in.WalletID = "other" },
		func(in *WagerInput) { in.Type = "WIN" }, func(in *WagerInput) { in.Amount = cents(t, 2001, "BRL") },
		func(in *WagerInput) { in.Amount = cents(t, 2000, "USD") }, func(in *WagerInput) { in.RoundID = "other" },
	} {
		in := base
		modify(&in)
		if CanonicalPayloadHash(in) == hash {
			t.Fatal("business field omitted")
		}
	}
}
