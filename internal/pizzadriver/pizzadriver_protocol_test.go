package pizzadriver

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeServer implements just enough of the PostgreSQL wire protocol to drive
// the driver: trust authentication, simple-query execution, and a controlled
// reaction to the second last_insert_rowid query (the post-insert followup).
type fakeServer struct {
	t   *testing.T
	ln  net.Listener
	dsn string

	mu      sync.Mutex
	inserts int
}

func startFakeServer(t *testing.T) *fakeServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	s := &fakeServer{
		t:   t,
		ln:  ln,
		dsn: fmt.Sprintf("host=127.0.0.1 port=%d user=u sslmode=disable", ln.Addr().(*net.TCPAddr).Port),
	}
	go s.serve()

	t.Cleanup(func() { _ = ln.Close() })
	return s
}

func (s *fakeServer) insertCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.inserts
}

func (s *fakeServer) serve() {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handle(conn)
	}
}

func (s *fakeServer) handle(nc net.Conn) {
	defer nc.Close()

	backend := pgproto3.NewBackend(nc, nc)
	if _, err := backend.ReceiveStartupMessage(); err != nil {
		return
	}

	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "pizzasql-test"})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: 2})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := backend.Flush(); err != nil {
		return
	}

	lastRowidQueries := 0
	for {
		msg, err := backend.Receive()
		if err != nil {
			return
		}

		switch m := msg.(type) {
		case *pgproto3.Query:
			q := m.String
			if q == "SELECT last_insert_rowid()" {
				lastRowidQueries++
				if lastRowidQueries == 1 {
					// Capability check during Connect: report a valid scalar.
					s.sendRow(backend, "last_insert_rowid", "0")
					continue
				}
				// Post-insert followup: fail it so the last insert ID is never
				// retrievable, without acknowledging anything.
				backend.Send(&pgproto3.ErrorResponse{
					Severity: "ERROR",
					Code:     "57P01",
					Message:  "admin shutdown",
				})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				_ = backend.Flush()
				continue
			}

			if strings.HasPrefix(q, "INSERT") {
				s.mu.Lock()
				s.inserts++
				s.mu.Unlock()
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("INSERT 0 1")})
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
				if err := backend.Flush(); err != nil {
					return
				}
				continue
			}

			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("OK")})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err := backend.Flush(); err != nil {
				return
			}
		case *pgproto3.Terminate:
			return
		}
	}
}

func (s *fakeServer) sendRow(backend *pgproto3.Backend, name, value string) {
	backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{
		Name:         []byte(name),
		DataTypeOID:  20, // int8
		DataTypeSize: 8,
		Format:       0, // text
	}}})
	backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(value)}})
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	_ = backend.Flush()
}

// connectDirect establishes a single driver connection through the fake server
// and returns it as a *connection for white-box assertions.
func connectDirect(t *testing.T, srv *fakeServer) *connection {
	t.Helper()

	connector, err := (&pizzaDriver{}).OpenConnector(srv.dsn)
	require.NoError(t, err)

	conn, err := connector.Connect(context.Background())
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	c, ok := conn.(*connection)
	require.True(t, ok)
	return c
}

// TestQueryResultNeverErrBadConnWhenNotRetryable proves that the retryable flag
// is the only thing that maps a failure to driver.ErrBadConn. The post-write
// followup always calls with retryable=false.
func TestQueryResultNeverErrBadConnWhenNotRetryable(t *testing.T) {
	srv := startFakeServer(t)
	c := connectDirect(t, srv)

	require.NoError(t, c.pg.Close(context.Background()))

	_, err := c.queryResult(context.Background(), "SELECT last_insert_rowid()", nil, false)
	require.Error(t, err)
	assert.False(t, errors.Is(err, driver.ErrBadConn), "retryable=false must never return driver.ErrBadConn")

	_, err = c.queryResult(context.Background(), "SELECT last_insert_rowid()", nil, true)
	assert.True(t, errors.Is(err, driver.ErrBadConn), "retryable=true maps the same condition to driver.ErrBadConn")
}

// TestExecDoesNotReplayInsertWhenFollowupFails proves that when the
// last_insert_rowid followup fails after the insert was acknowledged, the error
// is not driver.ErrBadConn and database/sql does not replay the insert.
func TestExecDoesNotReplayInsertWhenFollowupFails(t *testing.T) {
	srv := startFakeServer(t)

	db, err := sql.Open(DriverName, srv.dsn)
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })

	_, err = db.Exec("INSERT INTO t (name) VALUES (?)", "alpha")
	require.Error(t, err, "the followup failure must surface as an error")

	// The error must not look retryable, otherwise database/sql would have
	// already replayed the insert on a fresh connection.
	assert.False(t, errors.Is(err, driver.ErrBadConn), "followup failure must not surface driver.ErrBadConn")
	assert.Contains(t, err.Error(), "uncertain")

	// Allow the fake server to observe any (incorrect) replay before asserting.
	require.Eventually(t, func() bool { return srv.insertCount() > 0 }, time.Second, 10*time.Millisecond)
	assert.Equal(t, 1, srv.insertCount(), "insert must not have been replayed")
}
