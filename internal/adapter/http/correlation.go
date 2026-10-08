package httpadapter

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

const correlationIDHeader = "X-Correlation-ID"

// CorrelationMiddleware ensures every request has a correlation ID available
// in the request context, logs and response headers.
func CorrelationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := r.Header.Get(correlationIDHeader)
		if !validCorrelationID(correlationID) || len(r.Header.Values(correlationIDHeader)) != 1 {
			correlationID = newCorrelationID()
		}

		ctx := observability.WithCorrelationID(r.Context(), correlationID)
		w.Header().Set(correlationIDHeader, correlationID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validCorrelationID(id string) bool {
	if len(id) < 1 || len(id) > 128 {
		return false
	}
	return strings.IndexFunc(id, func(r rune) bool { return r < 33 || r > 126 }) == -1
}

func newCorrelationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
