package httpadapter

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/config"
	"github.com/junglegaming/backend-challenge-go/internal/identity"
	"github.com/junglegaming/backend-challenge-go/internal/observability"
)

func TestMetricsHTTPInternalOnlyDisabledAndNormalizedRoutes(t *testing.T) {
	m := observability.NewMetrics(config.Config{MetricsEnabled: true})
	auth := identity.NewAuthorizer()
	h := NewHandler(NewHealthHandler(&atomic.Bool{}, stubPinger{}, config.Config{}), slog.New(slog.NewJSONHandler(io.Discard, nil)), NewAuthMiddleware(authDouble{}, auth), &FinancialHandler{commands: &commandSpy{}, authorizer: auth}, NewMetricsHandler(m))
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {"provider", 403}, {"internal", 200}} {
		w := callAPI(h, "GET", "/metrics", tc.token, "", "")
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
		if tc.status == 200 && !strings.Contains(w.Header().Get("Content-Type"), "text/plain") {
			t.Fatal("not Prometheus format")
		}
	}
	for _, id := range []string{"financial-id-A", "financial-id-B"} {
		callAPI(h, "GET", "/wallets/"+id, "internal", "", "")
	}
	callAPI(h, "GET", "/does-not-exist/financial-id-C", "internal", "", "")
	w := callAPI(h, "GET", "/metrics", "internal", "", "")
	if !strings.Contains(w.Body.String(), `route="/wallets/{walletId}"`) || strings.Contains(w.Body.String(), "financial-id-") {
		t.Fatal("concrete ID in metric labels", w.Body.String())
	}
	disabled := observability.NewMetrics(config.Config{})
	h = NewHandler(nil, slog.Default(), NewAuthMiddleware(authDouble{}, auth), &FinancialHandler{}, NewMetricsHandler(disabled))
	if w := callAPI(h, "GET", "/metrics", "internal", "", ""); w.Code != 404 {
		t.Fatal("disabled endpoint", w.Code)
	}
}

func TestDrainWaitsForActiveHandlersAndRefusesNewWork(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	done := make(chan struct{})
	h := &drainingHandler{next: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-release; w.WriteHeader(200) })}
	go func() { defer close(done); h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil)) }()
	<-started
	h.stopAccepting()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != 503 {
		t.Fatal("accepted work after stop")
	}
	joined := make(chan struct{})
	go func() { h.active.Wait(); close(joined) }()
	select {
	case <-joined:
		t.Fatal("did not wait for active handler")
	default:
	}
	close(release)
	<-done
	<-joined
}

func TestCorrelationIDsRejectOversizedControlAndRepeatedHeaders(t *testing.T) {
	for _, id := range []string{strings.Repeat("x", 129), "line\nbreak", "has space"} {
		seen := ""
		h := CorrelationMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			seen, _ = observability.CorrelationIDFromContext(r.Context())
		}))
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set(correlationIDHeader, id)
		h.ServeHTTP(httptest.NewRecorder(), r)
		if seen == id || !validCorrelationID(seen) {
			t.Fatal("unsafe correlation accepted")
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Add(correlationIDHeader, "first")
	r.Header.Add(correlationIDHeader, "second")
	w := httptest.NewRecorder()
	CorrelationMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})).ServeHTTP(w, r)
	if w.Header().Get(correlationIDHeader) == "first" {
		t.Fatal("ambiguous repeated correlation accepted")
	}
}
