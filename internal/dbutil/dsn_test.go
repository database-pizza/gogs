package dbutil

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/conf"
)

func TestParsePostgreSQLHostPort(t *testing.T) {
	tests := []struct {
		info    string
		expHost string
		expPort string
	}{
		{info: "127.0.0.1:1234", expHost: "127.0.0.1", expPort: "1234"},
		{info: "127.0.0.1", expHost: "127.0.0.1", expPort: "5432"},
		{info: "[::1]:1234", expHost: "[::1]", expPort: "1234"},
		{info: "[::1]", expHost: "[::1]", expPort: "5432"},
		{info: "/tmp/pg.sock:1234", expHost: "/tmp/pg.sock", expPort: "1234"},
		{info: "/tmp/pg.sock", expHost: "/tmp/pg.sock", expPort: "5432"},
	}
	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			host, port := ParsePostgreSQLHostPort(test.info)
			assert.Equal(t, test.expHost, host)
			assert.Equal(t, test.expPort, port)
		})
	}
}

func TestParseMSSQLHostPort(t *testing.T) {
	tests := []struct {
		info    string
		expHost string
		expPort string
	}{
		{info: "127.0.0.1:1234", expHost: "127.0.0.1", expPort: "1234"},
		{info: "127.0.0.1,1234", expHost: "127.0.0.1", expPort: "1234"},
		{info: "127.0.0.1", expHost: "127.0.0.1", expPort: "1433"},
	}
	for _, test := range tests {
		t.Run("", func(t *testing.T) {
			host, port := ParseMSSQLHostPort(test.info)
			assert.Equal(t, test.expHost, host)
			assert.Equal(t, test.expPort, port)
		})
	}
}

func TestNewDSN(t *testing.T) {
	t.Run("bad dialect", func(t *testing.T) {
		_, err := NewDSN(conf.DatabaseOpts{
			Type: "bad_dialect",
		})
		assert.Equal(t, "unrecognized dialect: bad_dialect", fmt.Sprintf("%v", err))
	})

	tests := []struct {
		name    string
		opts    conf.DatabaseOpts
		wantDSN string
	}{
		{
			name: "mysql: unix",
			opts: conf.DatabaseOpts{
				Type:     "mysql",
				Host:     "/tmp/mysql.sock",
				Name:     "gogs",
				User:     "gogs",
				Password: "pa$$word",
			},
			wantDSN: "gogs:pa$$word@unix(/tmp/mysql.sock)/gogs?charset=utf8mb4&parseTime=true",
		},
		{
			name: "mysql: tcp",
			opts: conf.DatabaseOpts{
				Type:     "mysql",
				Host:     "localhost:3306",
				Name:     "gogs",
				User:     "gogs",
				Password: "pa$$word",
			},
			wantDSN: "gogs:pa$$word@tcp(localhost:3306)/gogs?charset=utf8mb4&parseTime=true",
		},

		{
			name: "postgres: unix",
			opts: conf.DatabaseOpts{
				Type:     "postgres",
				Host:     "/tmp/pg.sock",
				Name:     "gogs",
				Schema:   "test",
				User:     "gogs@local",
				Password: "pa$$word",
				SSLMode:  "disable",
			},
			wantDSN: "user='gogs@local' password='pa$$word' host='/tmp/pg.sock' port='5432' dbname='gogs' sslmode='disable' search_path='test' application_name='gogs'",
		},
		{
			name: "postgres: tcp",
			opts: conf.DatabaseOpts{
				Type:     "postgres",
				Host:     "127.0.0.1",
				Name:     "gogs",
				Schema:   "test",
				User:     "gogs@local",
				Password: "pa$$word",
				SSLMode:  "disable",
			},
			wantDSN: "user='gogs@local' password='pa$$word' host='127.0.0.1' port='5432' dbname='gogs' sslmode='disable' search_path='test' application_name='gogs'",
		},

		{
			name: "mssql",
			opts: conf.DatabaseOpts{
				Type:     "mssql",
				Host:     "127.0.0.1",
				Name:     "gogs",
				User:     "gogs@local",
				Password: "pa$$word",
			},
			wantDSN: "server=127.0.0.1; port=1433; database=gogs; user id=gogs@local; password=pa$$word;",
		},

		{
			name: "sqlite3",
			opts: conf.DatabaseOpts{
				Type: "sqlite3",
				Path: "/tmp/gogs.db",
			},
			wantDSN: "file:/tmp/gogs.db?cache=shared&mode=rwc",
		},

		{
			name: "pizzasql: tls required by default",
			opts: conf.DatabaseOpts{
				Type:     "pizzasql",
				Host:     "db.database.pizza:5432",
				Name:     "gogs",
				User:     "gogs",
				Password: "pa$$word",
			},
			wantDSN: "host='db.database.pizza' port='5432' user='gogs' password='pa$$word' dbname='gogs' sslmode='require'",
		},
		{
			name: "pizzasql: org database name",
			opts: conf.DatabaseOpts{
				Type:     "pizzasql",
				Host:     "127.0.0.1",
				Name:     "my-org/gogs",
				User:     "gogs",
				Password: "pa$$word",
				SSLMode:  "require",
			},
			wantDSN: "host='127.0.0.1' port='5432' user='gogs' password='pa$$word' dbname='my-org/gogs' sslmode='require'",
		},
		{
			name: "pizzasql: tls explicitly disabled",
			opts: conf.DatabaseOpts{
				Type:     "pizzasql",
				Host:     "127.0.0.1:5432",
				Name:     "gogs",
				User:     "gogs",
				Password: "pa$$word",
				SSLMode:  "disable",
			},
			wantDSN: "host='127.0.0.1' port='5432' user='gogs' password='pa$$word' dbname='gogs' sslmode='disable'",
		},
		{
			name: "pizzasql: escapes quotes and backslashes",
			opts: conf.DatabaseOpts{
				Type:     "pizzasql",
				Host:     "127.0.0.1:5432",
				Name:     "gogs",
				User:     `g'ogs`,
				Password: `p'a\ss\word`,
				SSLMode:  "require",
			},
			wantDSN: `host='127.0.0.1' port='5432' user='g\'ogs' password='p\'a\\ss\\word' dbname='gogs' sslmode='require'`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dsn, err := NewDSN(test.opts)
			require.NoError(t, err)
			assert.Equal(t, test.wantDSN, dsn)
		})
	}
}

func TestPoolSize(t *testing.T) {
	tests := []struct {
		name       string
		dbType     string
		configured int
		want       int
	}{
		{name: "native sqlite keeps configured", dbType: "sqlite3", configured: 30, want: 30},
		{name: "native postgres keeps configured", dbType: "postgres", configured: 10, want: 10},
		{name: "pizzasql caps default", dbType: "pizzasql", configured: 30, want: 3},
		{name: "pizzasql caps unset", dbType: "pizzasql", configured: 0, want: 3},
		{name: "pizzasql respects explicit lower", dbType: "pizzasql", configured: 2, want: 2},
		{name: "pizzasql caps excessive", dbType: "pizzasql", configured: 100, want: 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, PoolSize(test.dbType, test.configured))
		})
	}
}

func TestIdlePoolSize(t *testing.T) {
	tests := []struct {
		name       string
		dbType     string
		configured int
		want       int
	}{
		{name: "native sqlite keeps configured", dbType: "sqlite3", configured: 30, want: 30},
		{name: "pizzasql caps default", dbType: "pizzasql", configured: 30, want: 3},
		{name: "pizzasql preserves explicit zero", dbType: "pizzasql", configured: 0, want: 0},
		{name: "pizzasql respects explicit lower", dbType: "pizzasql", configured: 2, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, IdlePoolSize(test.dbType, test.configured))
		})
	}
}

func TestPizzaSQLSSLMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "empty defaults to require", mode: "", want: "require"},
		{name: "honors disable", mode: "disable", want: "disable"},
		{name: "honors require", mode: "require", want: "require"},
		{name: "honors verify-full", mode: "verify-full", want: "verify-full"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, PizzaSQLSSLMode(test.mode))
		})
	}
}

func TestQuotePGKeywordValue(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{name: "plain", value: "gogs", want: "'gogs'"},
		{name: "single quote", value: "g'ogs", want: `'g\'ogs'`},
		{name: "backslash", value: `g\ogs`, want: `'g\\ogs'`},
		{name: "both", value: `g'\ogs`, want: `'g\'\\ogs'`},
		{name: "empty", value: "", want: "''"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, quotePGKeywordValue(test.value))
		})
	}
}

func TestNewDSNPizzasqlRoundTrip(t *testing.T) {
	const (
		password = `p@$$'w\or"d`
		database = "my-org/gogs"
	)
	dsn, err := NewDSN(conf.DatabaseOpts{
		Type:     "pizzasql",
		Host:     "db.database.pizza:5432",
		Name:     database,
		User:     "gogs",
		Password: password,
		SSLMode:  "require",
	})
	require.NoError(t, err)

	config, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)

	assert.Equal(t, "db.database.pizza", config.Host)
	assert.Equal(t, uint16(5432), config.Port)
	assert.Equal(t, "gogs", config.User)
	assert.Equal(t, password, config.Password)
	assert.Equal(t, database, config.Database)
	require.NotNil(t, config.TLSConfig, "ssl mode require must enable TLS")
}

func TestNewDSNPizzasqlDisableTLSNil(t *testing.T) {
	dsn, err := NewDSN(conf.DatabaseOpts{
		Type:     "pizzasql",
		Host:     "127.0.0.1:5432",
		Name:     "gogs",
		User:     "gogs",
		Password: "pw",
		SSLMode:  "disable",
	})
	require.NoError(t, err)

	config, err := pgconn.ParseConfig(dsn)
	require.NoError(t, err)
	assert.Nil(t, config.TLSConfig, "ssl mode disable must not enable TLS")
}
