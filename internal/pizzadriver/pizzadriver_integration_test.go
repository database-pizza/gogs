package pizzadriver

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests run only when PIZZASQL_TEST_DSN points at a disposable local
// PizzaSQL database. They never run against production: the operator must set
// the variable explicitly, and each test works in its own uniquely named table.
func openTestDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("PIZZASQL_TEST_DSN")
	if dsn == "" {
		t.Skip("PIZZASQL_TEST_DSN not set; skipping PizzaSQL integration test")
	}

	db, err := sql.Open(DriverName, dsn)
	require.NoError(t, err)
	require.NoError(t, db.Ping(), "Ping must pass against an engine with last_insert_rowid support")

	t.Cleanup(func() { _ = db.Close() })
	return db
}

func uniqueTable(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("pizzadriver_%d", time.Now().UnixNano())
}

func createTestTable(t *testing.T, db *sql.DB, table string) {
	t.Helper()
	_, err := db.Exec("CREATE TABLE " + table + " (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, active INTEGER, score REAL, created DATETIME)")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.Exec("DROP TABLE IF EXISTS " + table) })
}

func TestIntegrationCRUD(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	res, err := db.Exec("INSERT INTO "+table+" (name, active, score) VALUES (?, ?, ?)", "alpha", true, 3.5)
	require.NoError(t, err)

	id, err := res.LastInsertId()
	require.NoError(t, err)
	require.True(t, id > 0)

	affected, err := res.RowsAffected()
	require.NoError(t, err)
	assert.Equal(t, int64(1), affected)

	var (
		gotName   string
		gotActive bool
		gotScore  float64
	)
	require.NoError(t, db.QueryRow("SELECT name, active, score FROM "+table+" WHERE id = ?", id).Scan(&gotName, &gotActive, &gotScore))
	assert.Equal(t, "alpha", gotName)
	assert.Equal(t, true, gotActive)
	assert.Equal(t, 3.5, gotScore)

	updated, err := db.Exec("UPDATE "+table+" SET name = ? WHERE id = ?", "beta", id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mustRowsAffected(t, updated))

	deleted, err := db.Exec("DELETE FROM "+table+" WHERE id = ?", id)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mustRowsAffected(t, deleted))

	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
	assert.Equal(t, 0, count)
}

func TestIntegrationLastInsertIDMatchesRowid(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	res, err := db.Exec("INSERT INTO "+table+" (name) VALUES (?)", "first")
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)

	var stored int64
	require.NoError(t, db.QueryRow("SELECT id FROM "+table+" WHERE name = ?", "first").Scan(&stored))
	assert.Equal(t, id, stored)
}

func TestIntegrationTransaction(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	tx, err := db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO "+table+" (name) VALUES (?)", "committed")
	require.NoError(t, err)
	require.NoError(t, tx.Commit())

	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table+" WHERE name = ?", "committed").Scan(&count))
	assert.Equal(t, 1, count)

	tx, err = db.Begin()
	require.NoError(t, err)
	_, err = tx.Exec("INSERT INTO "+table+" (name) VALUES (?)", "rolled back")
	require.NoError(t, err)
	require.NoError(t, tx.Rollback())

	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table+" WHERE name = ?", "rolled back").Scan(&count))
	assert.Equal(t, 0, count)
}

func TestIntegrationSessionLocalLastInsertID(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	ctx := context.Background()
	connA, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connA.Close() })

	connB, err := db.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = connB.Close() })

	resA, err := connA.ExecContext(ctx, "INSERT INTO "+table+" (name) VALUES (?)", "a")
	require.NoError(t, err)
	idA, err := resA.LastInsertId()
	require.NoError(t, err)
	require.True(t, idA > 0)

	resB, err := connB.ExecContext(ctx, "INSERT INTO "+table+" (name) VALUES (?)", "b")
	require.NoError(t, err)
	idB, err := resB.LastInsertId()
	require.NoError(t, err)
	require.True(t, idB > 0)

	assert.NotEqual(t, idA, idB)

	// last_insert_rowid must be session-local: each connection still reports its
	// own most recent insert, not the other connection's.
	var gotA, gotB int64
	require.NoError(t, connA.QueryRowContext(ctx, "SELECT last_insert_rowid()").Scan(&gotA))
	require.NoError(t, connB.QueryRowContext(ctx, "SELECT last_insert_rowid()").Scan(&gotB))
	assert.Equal(t, idA, gotA)
	assert.Equal(t, idB, gotB)
}

func TestIntegrationTimestampScanning(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	// Requires the engine to report DATETIME columns with a timestamp OID. If it
	// reports TEXT, this test fails and flags the engine-side mapping work.
	ts := time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)
	_, err := db.Exec("INSERT INTO "+table+" (name, created) VALUES (?, ?)", "ts", ts)
	require.NoError(t, err)

	var got time.Time
	require.NoError(t, db.QueryRow("SELECT created FROM "+table+" WHERE name = ?", "ts").Scan(&got))
	assert.Equal(t, ts, got)
}

func TestIntegrationInjection(t *testing.T) {
	db := openTestDB(t)
	table := uniqueTable(t)
	createTestTable(t, db, table)

	payload := "x'); DROP TABLE " + table + "; --"
	_, err := db.Exec("INSERT INTO "+table+" (name) VALUES (?)", payload)
	require.NoError(t, err)

	var got string
	require.NoError(t, db.QueryRow("SELECT name FROM "+table+" WHERE name = ?", payload).Scan(&got))
	assert.Equal(t, payload, got)

	var count int
	require.NoError(t, db.QueryRow("SELECT count(*) FROM "+table).Scan(&count))
	assert.Equal(t, 1, count, "injected DROP TABLE must not have executed")
}

func mustRowsAffected(t *testing.T, res sql.Result) int64 {
	t.Helper()
	n, err := res.RowsAffected()
	require.NoError(t, err)
	return n
}
