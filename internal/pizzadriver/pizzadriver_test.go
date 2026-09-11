package pizzadriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func nv(vals ...driver.Value) []driver.NamedValue {
	out := make([]driver.NamedValue, len(vals))
	for i, v := range vals {
		out[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return out
}

func TestDriverRegistered(t *testing.T) {
	assert.Contains(t, sql.Drivers(), DriverName)
}

func TestBind(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		args    []driver.NamedValue
		want    string
		wantErr string
	}{
		{
			name:  "no placeholders",
			query: "SELECT 1",
			want:  "SELECT 1",
		},
		{
			name:  "single integer placeholder",
			query: "SELECT * FROM t WHERE id = ?",
			args:  nv(int64(5)),
			want:  "SELECT * FROM t WHERE id = 5",
		},
		{
			name:  "multiple placeholders",
			query: "SELECT * FROM t WHERE a = ? AND b = ?",
			args:  nv(int64(1), "two"),
			want:  "SELECT * FROM t WHERE a = 1 AND b = 'two'",
		},
		{
			name:  "string with quote",
			query: "SELECT ?",
			args:  nv("O'Reilly"),
			want:  "SELECT 'O''Reilly'",
		},
		{
			name:  "injection attempt is escaped",
			query: "SELECT ?",
			args:  nv("'; DROP TABLE t; --"),
			want:  "SELECT '''; DROP TABLE t; --'",
		},
		{
			name:  "boolean parameters",
			query: "VALUES (?, ?)",
			args:  nv(true, false),
			want:  "VALUES (1, 0)",
		},
		{
			name:  "float parameter",
			query: "VALUES (?)",
			args:  nv(float64(1.5)),
			want:  "VALUES (1.5)",
		},
		{
			name:  "nil parameter",
			query: "VALUES (?)",
			args:  nv(nil),
			want:  "VALUES (NULL)",
		},
		{
			name:  "time parameter is RFC3339 string",
			query: "VALUES (?)",
			args:  nv(time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)),
			want:  "VALUES ('2024-05-06T07:08:09Z')",
		},
		{
			name:    "byte slice is rejected (no blob literal support)",
			query:   "VALUES (?)",
			args:    nv([]byte{0x00, 0xff, 0x0a}),
			wantErr: "binary parameters are unsupported",
		},
		{
			name:  "question mark in single-quoted string",
			query: "SELECT '?' FROM t",
			want:  "SELECT '?' FROM t",
		},
		{
			name:  "question mark in doubled-quoted string",
			query: "SELECT 'it''s ?' FROM t",
			want:  "SELECT 'it''s ?' FROM t",
		},
		{
			name:  "question mark in double-quoted identifier",
			query: `SELECT "a?b" FROM t`,
			want:  `SELECT "a?b" FROM t`,
		},
		{
			name:  "question mark in backtick identifier",
			query: "SELECT `a?b` FROM t",
			want:  "SELECT `a?b` FROM t",
		},
		{
			name:  "question mark in bracket identifier",
			query: "SELECT [a?b] FROM t",
			want:  "SELECT [a?b] FROM t",
		},
		{
			// Backtick identifiers have no doubled-quote escape in PizzaSQL's
			// lexer, so a doubled backtick is just two adjacent tokens.
			name:  "backtick identifier has no escape",
			query: "SELECT `a``b` FROM t",
			want:  "SELECT `a``b` FROM t",
		},
		{
			name:  "question mark in line comment",
			query: "SELECT 1 -- ?\nFROM t",
			want:  "SELECT 1 -- ?\nFROM t",
		},
		{
			name:  "question mark in block comment",
			query: "SELECT /* ? */ 1",
			want:  "SELECT /* ? */ 1",
		},
		{
			name:  "comment marker inside string",
			query: "SELECT 'a -- b', ?",
			args:  nv(int64(1)),
			want:  "SELECT 'a -- b', 1",
		},
		{
			name:  "semicolon inside string",
			query: "SELECT ';'",
			want:  "SELECT ';'",
		},
		{
			name:  "trailing semicolon and comment",
			query: "SELECT 1; -- done",
			want:  "SELECT 1; -- done",
		},
		{
			name:    "NUL in SQL",
			query:   "SELECT \x00",
			wantErr: "NUL in SQL",
		},
		{
			name:    "NUL in text parameter",
			query:   "SELECT ?",
			args:    nv("a\x00b"),
			wantErr: "NUL in text parameter",
		},
		{
			name:    "unterminated string",
			query:   "SELECT 'abc",
			wantErr: "unterminated quoted token",
		},
		{
			name:    "unterminated block comment",
			query:   "SELECT /* abc",
			wantErr: "unterminated comment",
		},
		{
			name:    "multiple statements",
			query:   "SELECT 1; SELECT 2",
			wantErr: "multiple statements",
		},
		{
			name:    "not enough parameters",
			query:   "SELECT ?, ?",
			args:    nv(int64(1)),
			wantErr: "not enough parameters",
		},
		{
			name:    "too many parameters",
			query:   "SELECT ?",
			args:    nv(int64(1), int64(2)),
			wantErr: "too many parameters",
		},
		{
			name:    "named parameters are unsupported",
			query:   "SELECT ?",
			args:    []driver.NamedValue{{Ordinal: 1, Name: "id", Value: int64(1)}},
			wantErr: "named parameters",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := bind(test.query, test.args)
			if test.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.want, got)
		})
	}
}

func TestSQLLiteral(t *testing.T) {
	t.Run("non-finite numbers are rejected", func(t *testing.T) {
		for _, v := range []float64{math.Inf(1), math.Inf(-1), math.NaN()} {
			_, err := sqlLiteral(v)
			require.Error(t, err)
		}
	})

	t.Run("unsupported type is rejected", func(t *testing.T) {
		_, err := sqlLiteral(struct{}{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "unsupported parameter type")
	})
}

func TestOpenConnectorDoesNotLeakPassword(t *testing.T) {
	// Secrets that stress the faulty redaction regex (escaped quotes and
	// backslashes). None may appear in the error, even under %+v.
	secrets := []string{
		"supersecret-password",
		"pa'ss\\word",
		`quote'and\backslash`,
	}

	for _, secret := range secrets {
		dsns := []string{
			"postgres://user:" + secret + "@localhost:99999/db?sslmode=disable",
			"host=localhost port=99999 user=u password='" + secret + "' sslmode=disable",
		}
		for _, dsn := range dsns {
			_, err := (&pizzaDriver{}).OpenConnector(dsn)
			require.Error(t, err)
			assert.NotContains(t, fmt.Sprintf("%v", err), secret, "DSN %q leaked secret via %%v", dsn)
			assert.NotContains(t, fmt.Sprintf("%+v", err), secret, "DSN %q leaked secret via %%+v", dsn)
		}
	}
}

func TestConnectDoesNotLeakCredentials(t *testing.T) {
	const (
		user     = "sensitive-tenant-user"
		database = "sensitive-org/sensitive-db"
		password = "supersecret-password"
	)

	// Port 1 on localhost is reserved and refused, so this fails without a
	// reachable server while still exercising the sanitized connect error path.
	connector, err := (&pizzaDriver{}).OpenConnector("host=127.0.0.1 port=1 user=" + user + " password=" + password + " dbname=" + database + " sslmode=disable connect_timeout=1")
	require.NoError(t, err)

	_, err = connector.Connect(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pizzasql")
	assert.NotContains(t, err.Error(), password)
	assert.NotContains(t, err.Error(), user)
	assert.NotContains(t, err.Error(), database)
}

func TestBeginTxRejectsUnsupportedOptions(t *testing.T) {
	c := &connection{}

	_, err := c.BeginTx(context.Background(), driver.TxOptions{Isolation: driver.IsolationLevel(sql.LevelSerializable)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")

	_, err = c.BeginTx(context.Background(), driver.TxOptions{ReadOnly: true})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported")
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		in   string
		want time.Time
	}{
		{"2024-05-06T07:08:09Z", time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)},
		{"2024-05-06 07:08:09", time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)},
		{"2024-05-06 07:08:09.5", time.Date(2024, 5, 6, 7, 8, 9, 500000000, time.UTC)},
		{"2024-05-06 07:08:09.123456", time.Date(2024, 5, 6, 7, 8, 9, 123456000, time.UTC)},
		{"2024-05-06", time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)},
	}
	for _, test := range tests {
		got, err := parseTime(test.in)
		require.NoError(t, err)
		assert.Equal(t, test.want, got)
	}

	_, err := parseTime("not-a-time")
	require.Error(t, err)
}
