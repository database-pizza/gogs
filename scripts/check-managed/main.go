// check-managed verifies the hosted connection without printing credentials or rows.
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	_ "gogs.io/gogs/internal/pizzadriver"
)

func main() {
	passwordFile := flag.String("password-file", "", "Protected file containing the scoped database key")
	flag.Parse()
	if err := check(*passwordFile); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func check(passwordFile string) error {
	key, err := os.ReadFile(passwordFile)
	if err != nil {
		return fmt.Errorf("read database credential file: %w", err)
	}
	dsn := url.URL{Scheme: "postgresql", Host: "127.0.0.1:5432", Path: "/gogs/gogs", User: url.UserPassword("u", strings.TrimSpace(string(key))), RawQuery: "sslmode=disable"}
	db, err := sql.Open("pizzasql", dsn.String())
	if err != nil {
		return fmt.Errorf("configure managed connection: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("managed proxy authentication and engine capability: %w", err)
	}
	fmt.Println("Managed proxy authentication and insert-ID capability: OK")
	var count int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM sqlite_master WHERE type = 'table'").Scan(&count); err != nil {
		return fmt.Errorf("managed schema discovery: %w", err)
	}
	fmt.Printf("Managed schema discovery: OK (%d tables)\n", count)
	table := fmt.Sprintf("gogs_deploy_check_%d", time.Now().UnixNano())
	if _, err := db.ExecContext(ctx, "CREATE TABLE "+table+" (id INTEGER PRIMARY KEY AUTOINCREMENT, value TEXT)"); err != nil {
		return fmt.Errorf("managed schema creation scope: %w", err)
	}
	defer func() {
		if _, err := db.ExecContext(context.Background(), "DROP TABLE "+table); err != nil {
			fmt.Fprintln(os.Stderr, "Deployment test table cleanup failed; remove the gogs_deploy_check_ table through the console.")
		}
	}()
	if _, err := db.ExecContext(ctx, "CREATE UNIQUE INDEX "+table+"_value ON "+table+" (value)"); err != nil {
		return fmt.Errorf("managed unique index creation: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("managed transaction begin: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+" (value) VALUES (?)", "quoted; -- deployment check"); err != nil {
		return fmt.Errorf("managed parameterized write: %w", err)
	}
	if err := tx.Rollback(); err != nil {
		return fmt.Errorf("managed rollback: %w", err)
	}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
		return fmt.Errorf("managed rollback verification failed")
	}
	if _, err := db.ExecContext(ctx, "DROP INDEX "+table+"_value"); err != nil {
		return fmt.Errorf("managed index removal scope: %w", err)
	}
	fmt.Println("Managed DDL, parameterized writes, and transaction rollback: OK")
	return nil
}
