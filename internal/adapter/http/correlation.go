package httpadapter

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"

	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

const correlationIDHeader = "X-Correlation-ID"

// CorrelationMiddleware ensures every request has a correlation ID available
// in the request context, logs and response headers.
func CorrelationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		correlationID := r.Header.Get(correlationIDHeader)
		if correlationID == "" {
			correlationID = newCorrelationID()
		}

		ctx := observability.WithCorrelationID(r.Context(), correlationID)
		w.Header().Set(correlationIDHeader, correlationID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func newCorrelationID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf[:])
}
