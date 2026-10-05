package observability

import "context"

type contextKey string

const (
	correlationIDKey contextKey = "correlationId"
	messageIDKey     contextKey = "messageId"
	transactionIDKey contextKey = "transactionId"
	walletIDKey      contextKey = "walletId"
	providerIDKey    contextKey = "providerId"
)

// WithCorrelationID stores the correlation ID in the context.
func WithCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationIDKey, correlationID)
}

// CorrelationIDFromContext returns the correlation ID when present.
func CorrelationIDFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(correlationIDKey).(string)
	return value, ok && value != ""
}

// WithMessageID stores a message ID for later logging enrichment.
func WithMessageID(ctx context.Context, messageID string) context.Context {
	return context.WithValue(ctx, messageIDKey, messageID)
}

// WithTransactionID stores a transaction ID for later logging enrichment.
func WithTransactionID(ctx context.Context, transactionID string) context.Context {
	return context.WithValue(ctx, transactionIDKey, transactionID)
}

// WithWalletID stores a wallet ID for later logging enrichment.
func WithWalletID(ctx context.Context, walletID string) context.Context {
	return context.WithValue(ctx, walletIDKey, walletID)
}

// WithProviderID stores a provider ID for later logging enrichment.
func WithProviderID(ctx context.Context, providerID string) context.Context {
	return context.WithValue(ctx, providerIDKey, providerID)
}

// FieldsFromContext extracts known observability identifiers from the context.
func FieldsFromContext(ctx context.Context) map[string]string {
	fields := make(map[string]string)

	if value, ok := CorrelationIDFromContext(ctx); ok {
		fields["correlationId"] = value
	}
	if value, ok := ctx.Value(messageIDKey).(string); ok && value != "" {
		fields["messageId"] = value
	}
	if value, ok := ctx.Value(transactionIDKey).(string); ok && value != "" {
		fields["transactionId"] = value
	}
	if value, ok := ctx.Value(walletIDKey).(string); ok && value != "" {
		fields["walletId"] = value
	}
	if value, ok := ctx.Value(providerIDKey).(string); ok && value != "" {
		fields["providerId"] = value
	}

	return fields
}
