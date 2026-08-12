package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	_ "github.com/lib/pq"

	"opl-cloud/services/internal/postgresmigrate"
	ledgerhttp "opl-cloud/services/ledger/internal/http"
	"opl-cloud/services/ledger/internal/ledger"
)

func main() {
	addr := os.Getenv("LEDGER_ADDR")
	if addr == "" {
		addr = ":8081"
	}

	databaseURL, err := storeDatabaseURL(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	token, err := internalServiceToken(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	store := ledger.Store(ledger.NewMemoryStore())
	if databaseURL != "" {
		db, err := sql.Open("postgres", databaseURL)
		if err != nil {
			log.Fatal(err)
		}
		postgresStore := ledger.NewPostgresStore(db)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := postgresStore.Install(ctx); err != nil {
			log.Fatal(err)
		}
		store = postgresStore
	}

	handler := ledgerhttp.NewServer(store, token)
	log.Printf("ledger listening on %s", addr)
	if err := newHTTPServer(addr, handler).ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr: addr, Handler: handler,
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second,
		WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute,
	}
}

func internalServiceToken(getenv func(string) string) (string, error) {
	token := getenv("OPL_INTERNAL_SERVICE_TOKEN")
	if getenv("NODE_ENV") == "production" && token == "" {
		return "", errors.New("OPL_INTERNAL_SERVICE_TOKEN is required in production")
	}
	return token, nil
}

func storeDatabaseURL(getenv func(string) string) (string, error) {
	databaseURL := getenv("DATABASE_URL")
	if getenv("NODE_ENV") == "production" && databaseURL == "" {
		return "", errors.New("DATABASE_URL is required for production Ledger persistence")
	}
	if databaseURL != "" {
		if err := postgresmigrate.ValidateTLS(databaseURL); err != nil && !localPostgresTestDatabaseURL(getenv, databaseURL) {
			return "", err
		}
	}
	return databaseURL, nil
}

func localPostgresTestDatabaseURL(getenv func(string) string, databaseURL string) bool {
	if getenv("NODE_ENV") == "production" || getenv("OPL_POSTGRES_TESTS") != "1" {
		return false
	}
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" || parsed.Host == "" || parsed.Fragment != "" {
		return false
	}
	query := parsed.Query()
	if len(query["sslmode"]) != 1 || query.Get("sslmode") != "disable" || query.Has("host") {
		return false
	}
	address := net.ParseIP(parsed.Hostname())
	return address != nil && address.IsLoopback()
}
