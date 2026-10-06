package application_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Fx publication tests must not claim an operator's preexisting Outbox backlog.
func isolatedPublisherDatabase(t *testing.T) string {
	t.Helper()
	database := os.Getenv("TEST_DATABASE_URL")
	if database == "" {
		t.Skip("set TEST_DATABASE_URL for real publisher lifecycle")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	connection, err := pgx.Connect(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("phase7_fx_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err := connection.Exec(cleanup, "DROP SCHEMA IF EXISTS "+quoted+" CASCADE")
		if err != nil {
			t.Error(err)
		}
		_ = connection.Close(cleanup)
	})
	if _, err := connection.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Exec(ctx, "SET search_path TO "+quoted); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"000001_app_metadata.up.sql", "000002_financial_persistence.up.sql", "000003_wager_idempotency.up.sql", "000004_reversals_pending_references.up.sql", "000005_outbox_publication_leases.up.sql"} {
		contents, err := os.ReadFile(filepath.Join("..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := connection.Exec(ctx, string(contents)); err != nil {
			t.Fatal(err)
		}
	}
	dsn, err := url.Parse(database)
	if err != nil {
		t.Fatal(err)
	}
	query := dsn.Query()
	query.Set("search_path", schema)
	dsn.RawQuery = query.Encode()
	return dsn.String()
}
