// Package pizzadriver transports SQLite-style SQL to PizzaSQL over the
// PostgreSQL simple-query protocol. It does not open a SQLite database.
package pizzadriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/glebarez/sqlite"
	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
	"gorm.io/gorm/callbacks"
)

// DriverName is the database/sql driver name registered by this package.
const DriverName = "pizzasql"

// Timeouts for connection lifecycle operations. They bound the time spent
// waiting for the network during teardown so a hung engine cannot block the
// pool from releasing resources.
const (
	closeTimeout  = 5 * time.Second
	finishTimeout = 30 * time.Second
)

// PostgreSQL data type OIDs used to convert wire values into Go values. The
// engine may report incomplete OIDs, so every other OID is returned as text and
// left for database/sql to convert.
const (
	oidDate        = 1082
	oidTimestamp   = 1114
	oidTimestamptz = 1184
	oidTime        = 1266
)

func init() {
	sql.Register(DriverName, &pizzaDriver{})
}

var (
	_ driver.Driver           = (*pizzaDriver)(nil)
	_ driver.DriverContext    = (*pizzaDriver)(nil)
	_ driver.Connector        = (*connector)(nil)
	_ driver.Conn             = (*connection)(nil)
	_ driver.ConnBeginTx      = (*connection)(nil)
	_ driver.ExecerContext    = (*connection)(nil)
	_ driver.QueryerContext   = (*connection)(nil)
	_ driver.Pinger           = (*connection)(nil)
	_ driver.SessionResetter  = (*connection)(nil)
	_ driver.Validator        = (*connection)(nil)
	_ driver.Stmt             = (*statement)(nil)
	_ driver.StmtExecContext  = (*statement)(nil)
	_ driver.StmtQueryContext = (*statement)(nil)
	_ driver.Tx               = (*transaction)(nil)
	_ driver.Result           = result{}
	_ driver.Rows             = (*rows)(nil)
	_ gorm.Dialector          = (*dialector)(nil)
)

type pizzaDriver struct{}

type connector struct {
	config *pgconn.Config
}

type connection struct {
	pg *pgconn.PgConn
}

type statement struct {
	c     *connection
	query string
}

type transaction struct {
	c *connection
}

type result struct {
	id, affected int64
}

type rows struct {
	fields []pgconn.FieldDescription
	values [][][]byte
	pos    int
}

func (d *pizzaDriver) Open(dsn string) (driver.Conn, error) {
	c, err := d.OpenConnector(dsn)
	if err != nil {
		return nil, err
	}
	return c.Connect(context.Background())
}

func (*pizzaDriver) OpenConnector(dsn string) (driver.Connector, error) {
	config, err := pgconn.ParseConfig(dsn)
	if err != nil {
		// ParseConfigError carries the raw ConnString and its redaction can leak
		// the suffix of a password containing an escaped quote, so never surface
		// the original error.
		return nil, errors.New("pizzasql: invalid connection configuration")
	}
	return &connector{config: config}, nil
}

func (*connector) Driver() driver.Driver { return &pizzaDriver{} }

func (c *connector) Connect(ctx context.Context) (driver.Conn, error) {
	pg, err := pgconn.ConnectConfig(ctx, c.config.Copy())
	if err != nil {
		return nil, sanitizeConnectError(err)
	}

	conn := &connection{pg: pg}
	if err := conn.checkCapability(ctx); err != nil {
		closeCtx, cancel := context.WithTimeout(context.Background(), closeTimeout)
		defer cancel()
		_ = pg.Close(closeCtx)
		return nil, err
	}
	return conn, nil
}

// sanitizeConnectError preserves an actionable SQLSTATE from a failed
// connection attempt while dropping every component of the DSN (user, database,
// and password) that identifies the tenant.
func sanitizeConnectError(err error) error {
	var connErr *pgconn.ConnectError
	if !errors.As(err, &connErr) {
		return errors.New("pizzasql: connection failed")
	}

	var pgErr *pgconn.PgError
	if errors.As(connErr, &pgErr) {
		return errors.Errorf("pizzasql: connection failed (SQLSTATE %s)", pgErr.Code)
	}
	return errors.New("pizzasql: connection failed; check address, TLS settings, and credentials")
}

// checkCapability verifies that the engine implements a session-local
// last_insert_rowid function. Older engines return NULL or an error here, so
// this fails fast instead of silently producing wrong primary keys.
func (c *connection) checkCapability(ctx context.Context) error {
	r, err := c.query(ctx, "SELECT last_insert_rowid()", nil)
	if err != nil {
		return err
	}
	if len(r.Rows) != 1 || len(r.Rows[0]) != 1 || r.Rows[0][0] == nil {
		return errors.New("pizzasql: engine does not support a session-local last_insert_rowid")
	}
	if _, err := strconv.ParseInt(string(r.Rows[0][0]), 10, 64); err != nil {
		return errors.Wrap(err, "pizzasql: engine returned a non-integer last_insert_rowid")
	}
	return nil
}

func (c *connection) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), closeTimeout)
	defer cancel()
	return c.pg.Close(ctx)
}

func (c *connection) IsValid() bool {
	return !c.pg.IsClosed() && c.pg.TxStatus() == 'I'
}

func (c *connection) ResetSession(context.Context) error {
	if !c.IsValid() {
		return driver.ErrBadConn
	}
	return nil
}

func (c *connection) Ping(ctx context.Context) error {
	return c.checkCapability(ctx)
}

func (c *connection) Prepare(query string) (driver.Stmt, error) {
	return &statement{c: c, query: query}, nil
}

func (s *statement) Close() error  { return nil }
func (s *statement) NumInput() int { return -1 }

func named(args []driver.Value) []driver.NamedValue {
	values := make([]driver.NamedValue, len(args))
	for i, v := range args {
		values[i] = driver.NamedValue{Ordinal: i + 1, Value: v}
	}
	return values
}

func (s *statement) Exec(args []driver.Value) (driver.Result, error) {
	return s.ExecContext(context.Background(), named(args))
}

func (s *statement) Query(args []driver.Value) (driver.Rows, error) {
	return s.QueryContext(context.Background(), named(args))
}

func (s *statement) ExecContext(ctx context.Context, args []driver.NamedValue) (driver.Result, error) {
	return s.c.ExecContext(ctx, s.query, args)
}

func (s *statement) QueryContext(ctx context.Context, args []driver.NamedValue) (driver.Rows, error) {
	return s.c.QueryContext(ctx, s.query, args)
}

func (c *connection) Begin() (driver.Tx, error) {
	return c.BeginTx(context.Background(), driver.TxOptions{})
}

func (c *connection) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	if opts.Isolation != 0 || opts.ReadOnly {
		return nil, errors.New("pizzasql: explicit transaction isolation and read-only transactions are unsupported")
	}
	if _, err := c.query(ctx, "BEGIN", nil); err != nil {
		return nil, err
	}
	return &transaction{c: c}, nil
}

func (t *transaction) finish(query string) error {
	ctx, cancel := context.WithTimeout(context.Background(), finishTimeout)
	defer cancel()
	_, err := t.c.query(ctx, query, nil)
	return err
}

func (t *transaction) Commit() error   { return t.finish("COMMIT") }
func (t *transaction) Rollback() error { return t.finish("ROLLBACK") }

// query executes a single statement through the simple-query protocol and
// returns its result. Errors that are guaranteed to have occurred before
// anything was sent are surfaced as driver.ErrBadConn so database/sql retries
// on a fresh connection.
func (c *connection) query(ctx context.Context, query string, args []driver.NamedValue) (*pgconn.Result, error) {
	return c.queryResult(ctx, query, args, true)
}

// queryResult executes a single statement through the simple-query protocol.
// When retryable is false, no error is mapped to driver.ErrBadConn, so a caller
// that has already acknowledged a write never triggers a replay of that write.
func (c *connection) queryResult(ctx context.Context, query string, args []driver.NamedValue, retryable bool) (*pgconn.Result, error) {
	if c.pg.IsClosed() {
		if retryable {
			return nil, driver.ErrBadConn
		}
		return nil, errors.New("pizzasql: connection is closed")
	}

	sql, err := bind(query, args)
	if err != nil {
		return nil, err
	}

	results, err := c.pg.Exec(ctx, sql).ReadAll()
	if err != nil {
		if retryable && pgconn.SafeToRetry(err) {
			return nil, driver.ErrBadConn
		}
		return nil, err
	}
	if len(results) != 1 {
		return nil, errors.New("pizzasql: expected one statement result")
	}
	return results[0], results[0].Err
}

func (c *connection) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	r, err := c.query(ctx, query, args)
	if err != nil {
		return nil, err
	}

	res := result{affected: r.CommandTag.RowsAffected()}
	if r.CommandTag.Insert() && res.affected > 0 {
		// The insert has already been acknowledged, so a failure to read the
		// last insert ID must never surface driver.ErrBadConn: database/sql
		// would interpret it as retryable and replay the whole insert.
		id, err := c.queryResult(ctx, "SELECT last_insert_rowid()", nil, false)
		if err != nil {
			return nil, errors.Wrap(err, "pizzasql: insert acknowledged but last insert ID could not be retrieved; the row state is uncertain")
		}
		if len(id.Rows) != 1 || len(id.Rows[0]) != 1 || id.Rows[0][0] == nil {
			return nil, errors.New("pizzasql: invalid last insert ID result")
		}
		res.id, err = strconv.ParseInt(string(id.Rows[0][0]), 10, 64)
		if err != nil {
			return nil, errors.Wrap(err, "pizzasql: server did not return an integer insert ID")
		}
	}
	return res, nil
}

func (r result) LastInsertId() (int64, error) { return r.id, nil }
func (r result) RowsAffected() (int64, error) { return r.affected, nil }

func (c *connection) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	r, err := c.query(ctx, query, args)
	if err != nil {
		return nil, err
	}
	return &rows{fields: r.FieldDescriptions, values: r.Rows}, nil
}

func (r *rows) Columns() []string {
	names := make([]string, len(r.fields))
	for i, f := range r.fields {
		names[i] = f.Name
	}
	return names
}

func (r *rows) Close() error {
	r.values = nil
	return nil
}

func (r *rows) Next(dest []driver.Value) error {
	if r.pos >= len(r.values) {
		return io.EOF
	}

	row := r.values[r.pos]
	for i, v := range row {
		if v == nil {
			dest[i] = nil
			continue
		}

		switch r.fields[i].DataTypeOID {
		case oidTimestamp, oidTimestamptz, oidDate, oidTime:
			t, err := parseTime(string(v))
			if err != nil {
				return err
			}
			dest[i] = t
		default:
			// Leave text, integer, float, and boolean values as strings and let
			// database/sql convert them based on the scan destination. This
			// tolerates the engine's incomplete OID reporting.
			dest[i] = string(v)
		}
	}
	r.pos++
	return nil
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"15:04:05.999999999",
	"15:04:05",
}

func parseTime(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, errors.Errorf("pizzasql: invalid timestamp value %q", s)
}

// bind inlines the given parameters in place of ? placeholders, quoting each
// value with SQLite syntax so the resulting statement is safe to send over the
// simple-query protocol. Placeholders inside string literals, quoted
// identifiers, and comments are left untouched.
func bind(query string, args []driver.NamedValue) (string, error) {
	var out strings.Builder
	n := 0
	ended := false

	for i := 0; i < len(query); {
		ch := query[i]

		if ch == 0 {
			return "", errors.New("pizzasql: NUL in SQL")
		}

		if ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n' {
			out.WriteByte(ch)
			i++
			continue
		}

		if i+1 < len(query) && query[i:i+2] == "--" {
			end := strings.IndexByte(query[i:], '\n')
			if end < 0 {
				end = len(query) - i
			}
			out.WriteString(query[i : i+end])
			i += end
			continue
		}

		if i+1 < len(query) && query[i:i+2] == "/*" {
			end := strings.Index(query[i+2:], "*/")
			if end < 0 {
				return "", errors.New("pizzasql: unterminated comment")
			}
			end += i + 4
			out.WriteString(query[i:end])
			i = end
			continue
		}

		if ended {
			return "", errors.New("pizzasql: multiple statements are unsupported")
		}

		if ch == ';' {
			ended = true
			out.WriteByte(ch)
			i++
			continue
		}

		if ch == '\'' || ch == '"' || ch == '`' || ch == '[' {
			end, err := scanQuoted(query, i)
			if err != nil {
				return "", err
			}
			out.WriteString(query[i:end])
			i = end
			continue
		}

		if ch == '?' {
			if n >= len(args) {
				return "", errors.New("pizzasql: not enough parameters")
			}
			if args[n].Name != "" {
				return "", errors.New("pizzasql: named parameters are unsupported")
			}
			literal, err := sqlLiteral(args[n].Value)
			if err != nil {
				return "", err
			}
			out.WriteString(literal)
			n++
			i++
			continue
		}

		out.WriteByte(ch)
		i++
	}

	if n != len(args) {
		return "", errors.New("pizzasql: too many parameters")
	}
	return out.String(), nil
}

// scanQuoted returns the index just past the quoted token that begins at start,
// which must point at ', ", `, or [. It verifies the token is terminated.
//
// Only string literals treat a doubled quote as an escape, matching PizzaSQL's
// lexer. The lexer does not support escaping inside quoted or backtick
// identifiers, so identifiers that contain their own delimiter are not escaped
// here either. Gogs's static ORM identifiers never need this, so it is kept
// honest rather than faking compatibility.
func scanQuoted(query string, start int) (int, error) {
	delim := query[start]
	end := delim
	escaped := delim == '\''
	if end == '[' {
		end = ']'
	}

	i := start + 1
	for i < len(query) {
		if query[i] != end {
			i++
			continue
		}
		i++
		if escaped && i < len(query) && query[i] == end {
			i++
			continue
		}
		return i, nil
	}
	return 0, errors.New("pizzasql: unterminated quoted token")
}

func sqlLiteral(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "NULL", nil
	case bool:
		if v {
			return "1", nil
		}
		return "0", nil
	case int64:
		return strconv.FormatInt(v, 10), nil
	case float64:
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return "", errors.New("pizzasql: non-finite number")
		}
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	case time.Time:
		return sqlLiteral(v.UTC().Format(time.RFC3339Nano))
	case []byte:
		// PizzaSQL has no X'...' blob literal support, so silently turning the
		// bytes into a hex text string would corrupt data. Reject honestly until
		// the engine supports blobs. Gogs models have no []byte columns.
		return "", errors.New("pizzasql: binary parameters are unsupported by the engine")
	case string:
		if strings.ContainsRune(v, 0) {
			return "", errors.New("pizzasql: NUL in text parameter")
		}
		return "'" + strings.ReplaceAll(v, "'", "''") + "'", nil
	default:
		return "", errors.Errorf("pizzasql: unsupported parameter type %T", value)
	}
}

type dialector struct {
	sqlite.Dialector
}

// Open returns a GORM dialector that speaks the SQLite dialect over the
// pizzasql transport.
func Open(dsn string) gorm.Dialector {
	return &dialector{sqlite.Dialector{DriverName: DriverName, DSN: dsn}}
}

func (d *dialector) Initialize(db *gorm.DB) error {
	conn, err := sql.Open(DriverName, d.DSN)
	if err != nil {
		return err
	}
	db.ConnPool = conn

	// Fail fast when the engine cannot provide session-local insert IDs instead
	// of deferring the failure to the first write. Do not claim a sqlite_version
	// capability, which an engine could fake.
	if sqlDB, ok := db.ConnPool.(*sql.DB); ok {
		if err := sqlDB.PingContext(context.Background()); err != nil {
			_ = sqlDB.Close()
			return errors.Wrap(err, "pizzasql: verify engine capability")
		}
	}

	// PizzaSQL supports last-insert IDs but not SQLite's RETURNING clause, so
	// register callbacks without RETURNING and recover primary keys by reversing
	// the last insert ID for batch creates.
	callbacks.RegisterDefaultCallbacks(db, &callbacks.Config{LastInsertIDReversed: true})

	for name, builder := range d.ClauseBuilders() {
		db.ClauseBuilders[name] = builder
	}
	return nil
}
