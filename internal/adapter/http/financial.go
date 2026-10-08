package httpadapter

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/junglegaming/backend-challenge-go/internal/application/financial"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wallet"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
	"github.com/junglegaming/backend-challenge-go/internal/ports"
)

const maxJSONBody = 64 << 10

type financialCommands interface {
	CreateWallet(context.Context, identity.Principal, financial.CreateWalletInput) (financial.CreateWalletResult, error)
	GetWallet(context.Context, identity.Principal, wallet.ID) (wallet.Wallet, error)
	ProcessHTTPWager(context.Context, identity.Principal, financial.HTTPWagerInput) (financial.WagerResult, error)
}
type financialReads interface {
	Ledger(context.Context, identity.Principal, wallet.ID, string, int) (financial.LedgerPage, error)
	Transaction(context.Context, identity.Principal, wager.TransactionID) (financial.TransactionDetail, error)
	ExternalTransaction(context.Context, identity.Principal, wager.ProviderID, wager.ExternalTransactionID) (financial.TransactionDetail, error)
}

type financialReconciliation interface {
	Reconcile(context.Context, identity.Principal, wallet.ID) (financial.ReconciliationResult, error)
}

type FinancialHandler struct {
	commands       financialCommands
	reads          financialReads
	reconciliation financialReconciliation
	authorizer     *identity.Authorizer
}

func NewFinancialHandler(commands *financial.AuthorizedService, reads *financial.ReadService, reconciliation *financial.ReconciliationService, auth *identity.Authorizer) *FinancialHandler {
	return &FinancialHandler{commands: commands, reads: reads, reconciliation: reconciliation, authorizer: auth}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, out any) bool {
	values := r.Header.Values("Content-Type")
	if len(values) != 1 {
		WriteError(w, 415, "UNSUPPORTED_MEDIA_TYPE", "application/json is required")
		return false
	}
	media, params, err := mime.ParseMediaType(values[0])
	if err != nil || media != "application/json" || (params["charset"] != "" && !strings.EqualFold(params["charset"], "utf-8")) {
		WriteError(w, 415, "UNSUPPORTED_MEDIA_TYPE", "application/json is required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	err = d.Decode(out)
	if err == nil {
		var extra any
		if next := d.Decode(&extra); next != io.EOF {
			err = next
			if err == nil {
				err = errors.New("multiple JSON values")
			}
		}
	}
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			WriteError(w, 413, "PAYLOAD_TOO_LARGE", "request body exceeds 64 KiB")
		} else {
			WriteError(w, 400, "INVALID_REQUEST", "invalid JSON request")
		}
		return false
	}
	return true
}

// Fixed scale is a transport contract. Arithmetic and overflow checking remain
// in Money; no float parsing or financial rule is implemented in this adapter.
func parseHTTPMoney(m money.External) (money.Money, error) {
	dot := strings.IndexByte(m.Amount, '.')
	if dot < 1 || dot != len(m.Amount)-3 {
		return money.Money{}, financial.ErrInvalidAmount
	}
	for i, c := range m.Amount {
		if i == dot {
			continue
		}
		if c < '0' || c > '9' {
			return money.Money{}, financial.ErrInvalidAmount
		}
	}
	v, err := money.ParseDecimal(m.Amount, m.Currency)
	if err != nil {
		return money.Money{}, financial.ErrInvalidAmount
	}
	return v, nil
}

func validIdentifier(id string) bool {
	if id == "" || len(id) > 256 || !utf8.ValidString(id) || strings.ContainsAny(id, "/\\?#") {
		return false
	}
	for _, c := range id {
		if unicode.IsControl(c) || unicode.IsSpace(c) {
			return false
		}
	}
	return true
}
func requireIDs(w http.ResponseWriter, ids ...string) bool {
	for _, id := range ids {
		if !validIdentifier(id) {
			WriteError(w, 400, "INVALID_REQUEST", "invalid resource identifier")
			return false
		}
	}
	return true
}
func parseLedgerQuery(r *http.Request) (url.Values, error) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	for key := range q {
		if key != "limit" && key != "cursor" {
			return nil, financial.ErrInvalidPagination
		}
	}
	return q, nil
}
func principal(r *http.Request) identity.Principal {
	p, _ := identity.FromContext(r.Context())
	return p
}
func correlation(r *http.Request) string {
	id, _ := observability.CorrelationIDFromContext(r.Context())
	return id
}

func writeApplicationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, identity.ErrUnauthorized), errors.Is(err, identity.ErrForbidden):
		writeAuthError(w, err)
	case errors.Is(err, ports.ErrNotFound), errors.Is(err, financial.ErrWalletNotFound):
		WriteError(w, 404, ErrorCodeNotFound, "resource not found")
	case errors.Is(err, financial.ErrWalletExists):
		WriteError(w, 409, "WALLET_EXISTS", "wallet already exists for player and currency")
	case errors.Is(err, financial.ErrIdempotencyConflict):
		WriteError(w, 409, "IDEMPOTENCY_CONFLICT", "idempotency key was used with a different payload")
	case errors.Is(err, financial.ErrDuplicateExternalTransaction):
		WriteError(w, 409, "DUPLICATE_EXTERNAL_TRANSACTION", "external transaction already exists")
	case errors.Is(err, financial.ErrIdempotencyKeyRequired):
		WriteError(w, 400, "IDEMPOTENCY_KEY_REQUIRED", "Idempotency-Key is required")
	case errors.Is(err, financial.ErrInvalidPagination):
		WriteError(w, 400, "INVALID_PAGINATION", "invalid ledger cursor or limit")
	case errors.Is(err, financial.ErrInvalidAmount), errors.Is(err, financial.ErrInvalidInput), errors.Is(err, financial.ErrCurrencyMismatch), errors.Is(err, financial.ErrPlayerMismatch), errors.Is(err, financial.ErrInvalidOperationType):
		WriteError(w, 400, "INVALID_REQUEST", "invalid financial request")
	case errors.Is(err, financial.ErrPersistence), errors.Is(err, ports.ErrPersistence), errors.Is(err, ports.ErrTransaction), errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		WriteError(w, 503, "SERVICE_UNAVAILABLE", "financial dependency unavailable")
	default:
		WriteError(w, 500, ErrorCodeInternal, "request could not be completed")
	}
}
