package database

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/cockroachdb/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"gogs.io/gogs/internal/conf"
	"gogs.io/gogs/internal/lfsutil"
)

// Everything written below is synthetic: fixed fake users, repositories, OIDs,
// and a fixed fake credential. No real credentials, paths, or tenant data are
// ever read from the environment or written to the engine.
const (
	syntheticPassword = "synthetic-test-password"
	smokeOIDAlpha     = lfsutil.OID("ef797c8118f02dfb649607dd5d3f8c7623048c9c063d532cc95c5ed7a898a64f")
	smokeOIDBeta      = lfsutil.OID("deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")
)

// lockedBuffer is a concurrency-safe capture buffer for the child processes.
// Both engines log from multiple goroutines, so writes are serialized before
// the buffer is inspected on failure.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// tailLines returns the last n lines of s so a failing assertion dumps the
// exact SQL the engine rejected instead of the full startup log.
func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// childProcess is a single engine child process owned by this test. stop only
// signals and reaps its own process, never a broader process group.
type childProcess struct {
	name     string
	cmd      *exec.Cmd
	stopOnce sync.Once
}

func (c *childProcess) stop() {
	c.stopOnce.Do(func() {
		if c.cmd == nil || c.cmd.Process == nil {
			return
		}
		_ = c.cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan error, 1)
		go func() { done <- c.cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = c.cmd.Process.Kill()
			<-done
		}
	})
}

// requireEngineBinaries validates the opt-in env vars. Unset means the smoke
// test is not opted in (skip). A set-but-invalid path is a configuration error
// (fatal), never silently skipped.
func requireEngineBinaries(t *testing.T) (sqlBin, kvBin string) {
	t.Helper()
	sqlBin = os.Getenv("PIZZASQL_BIN")
	kvBin = os.Getenv("PIZZAKV_BIN")
	if sqlBin == "" && kvBin == "" {
		t.Skip("set both PIZZASQL_BIN and PIZZAKV_BIN to absolute engine binary paths to run the PizzaSQL smoke test")
	}
	for _, bin := range []struct{ name, path string }{
		{"PIZZASQL_BIN", sqlBin},
		{"PIZZAKV_BIN", kvBin},
	} {
		if bin.path == "" {
			t.Fatalf("%s is not set", bin.name)
		}
		if !filepath.IsAbs(bin.path) {
			t.Fatalf("%s must be an absolute path, got %q", bin.name, bin.path)
		}
		if info, err := os.Stat(bin.path); err != nil {
			t.Fatalf("%s %q is not usable: %v", bin.name, bin.path, err)
		} else if info.IsDir() {
			t.Fatalf("%s %q is a directory, not a binary", bin.name, bin.path)
		}
	}
	return sqlBin, kvBin
}

// TestPizzaSQLSmokeIntegration is the opt-in entry point. It validates the
// engine binaries and then re-executes this test binary in a subprocess so that
// conf.Init, which mutates many package globals and sync.Once caches, cannot
// leak into the other tests in this package.
func TestPizzaSQLSmokeIntegration(t *testing.T) {
	requireEngineBinaries(t)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestPizzaSQLSmokeChild$", "-test.v")
	cmd.Env = append(os.Environ(), "PIZZASQL_SMOKE_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("PizzaSQL smoke subprocess failed: %v\n%s", err, tailLines(string(out), 500))
	}
	if testing.Verbose() {
		t.Logf("=== PizzaSQL smoke subprocess output (synthetic test data only) ===\n%s", string(out))
	}
}

// TestPizzaSQLSmokeChild runs the actual smoke test inside the isolated
// subprocess. It is skipped when run directly as part of the full package suite.
func TestPizzaSQLSmokeChild(t *testing.T) {
	if os.Getenv("PIZZASQL_SMOKE_CHILD") != "1" {
		t.Skip("driven by TestPizzaSQLSmokeIntegration in a subprocess")
	}
	runPizzaSQLSmoke(t)
}

func runPizzaSQLSmoke(t *testing.T) {
	sqlBin := os.Getenv("PIZZASQL_BIN")
	kvBin := os.Getenv("PIZZAKV_BIN")
	require.NotEmpty(t, sqlBin, "PIZZASQL_BIN must be set")
	require.NotEmpty(t, kvBin, "PIZZAKV_BIN must be set")

	dir := t.TempDir()
	kvPath := filepath.Join(dir, "gogs.pkvdb")
	kvPort := freePort(t)
	pgPort := freePort(t)

	logs := &lockedBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("=== pizzakv/pizzasql output (tail, synthetic test data only) ===\n%s", tailLines(logs.String(), 400))
		}
	})
	t.Cleanup(func() { closePools(t) })

	configurePizzaSQL(t, dir, pgPort)

	ctx := context.Background()

	t.Log("stage 1: fresh startup installs the schema through the real startup path")
	kv := startPizzaKV(t, kvBin, kvPort, kvPath, logs)
	sql := startPizzaSQL(t, sqlBin, kvPort, pgPort, logs)
	require.NoError(t, NewEngine(), "initial NewEngine (GORM migrate + xorm Sync2)")
	require.NotNil(t, Handle, "global GORM handle must be set")
	require.NotNil(t, x, "global xorm engine must be set")

	require.True(t, Handle.db.Migrator().HasTable(new(AccessToken)), "GORM must create the access_token table")
	require.True(t, Handle.db.Migrator().HasTable(new(LFSObject)), "GORM must create the lfs_object table")
	userTable, err := x.IsTableExist(new(User))
	require.NoError(t, err, "check user table via xorm")
	require.True(t, userTable, "xorm Sync2 must create the user table")
	repoTable, err := x.IsTableExist(new(Repository))
	require.NoError(t, err, "check repository table via xorm")
	require.True(t, repoTable, "xorm Sync2 must create the repository table")

	var version struct {
		ID      int64
		Version int64
	}
	require.NoError(t, Handle.db.Table("version").Where("id = ?", 1).First(&version).Error, "migrations.Migrate must seed the version table")
	require.GreaterOrEqual(t, version.Version, int64(19), "migration version must be at least the minimum supported version")

	t.Log("stage 2: write representative records through the public GORM stores")
	alice, err := Handle.Users().Create(ctx, "alice", "alice@example.com", CreateUserOptions{Password: syntheticPassword, Activated: true})
	require.NoError(t, err, "create user alice via UsersStore")
	require.True(t, alice.ID > 0, "generated user ID must be positive")

	bob, err := Handle.Users().Create(ctx, "bob", "bob@example.com", CreateUserOptions{Password: syntheticPassword, Activated: true})
	require.NoError(t, err, "create user bob via UsersStore")
	require.True(t, bob.ID > 0, "generated user ID must be positive")

	repo, err := Handle.Repositories().Create(ctx, alice.ID, CreateRepoOptions{
		Name:         "hello",
		Description:  "smoke repository",
		EnableWiki:   true,
		EnableIssues: true,
		EnablePulls:  true,
	})
	require.NoError(t, err, "create repository via RepositoriesStore")
	require.True(t, repo.ID > 0, "generated repository ID must be positive")

	repo2, err := Handle.Repositories().Create(ctx, alice.ID, CreateRepoOptions{Name: "second"})
	require.NoError(t, err, "create a second repository")
	require.True(t, repo2.ID > 0, "generated repository ID must be positive")

	token, err := Handle.AccessTokens().Create(ctx, alice.ID, "smoke-token")
	require.NoError(t, err, "create access token via AccessTokensStore")
	require.True(t, token.ID > 0, "generated access token ID must be positive")

	require.NoError(t, Handle.Permissions().SetRepoPerms(ctx, repo.ID, map[int64]AccessMode{
		bob.ID: AccessModeWrite,
	}), "set repository permissions via PermissionsStore")

	t.Log("stage 3: cross-ORM reads and intended-store reads")
	// User and Repository are legacy tables shared by both ORMs in production,
	// so a GORM-store write read back through xorm is the real startup behavior.
	var gotUser User
	ok, err := x.ID(alice.ID).Get(&gotUser)
	require.NoError(t, err, "read user alice via xorm")
	require.True(t, ok, "user alice must exist via xorm")
	assert.Equal(t, alice.ID, gotUser.ID)
	assert.Equal(t, "alice", gotUser.Name)
	assert.Equal(t, "alice", gotUser.LowerName)

	var gotRepo Repository
	ok, err = x.ID(repo.ID).Get(&gotRepo)
	require.NoError(t, err, "read repository via xorm")
	require.True(t, ok, "repository must exist via xorm")
	assert.Equal(t, repo.ID, gotRepo.ID)
	assert.Equal(t, alice.ID, gotRepo.OwnerID)
	assert.Equal(t, "hello", gotRepo.Name)

	// Access is read by xorm in production (x.Count(new(Access))), and its
	// fields (ID, UserID, RepoID, Mode) map cleanly under the Gonic mapper, so
	// reading the GORM-written row back through xorm is safe.
	var gotAccess Access
	ok, err = x.Where("user_id = ? AND repo_id = ?", bob.ID, repo.ID).Get(&gotAccess)
	require.NoError(t, err, "read access via xorm")
	require.True(t, ok, "access row must exist via xorm")
	assert.Equal(t, bob.ID, gotAccess.UserID)
	assert.Equal(t, repo.ID, gotAccess.RepoID)
	assert.Equal(t, AccessModeWrite, gotAccess.Mode)

	// AccessToken is a GORM-only model: read it back through its intended GORM
	// store rather than a contrived xorm read.
	listedTokens, err := Handle.AccessTokens().List(ctx, alice.ID)
	require.NoError(t, err, "list access tokens via AccessTokensStore")
	require.Len(t, listedTokens, 1)
	assert.Equal(t, "smoke-token", listedTokens[0].Name)
	assert.Len(t, listedTokens[0].SHA256, 64, "stored SHA256 must be a 64-char hex digest")

	t.Log("stage 4: uniqueness and transaction rollback")
	_, err = Handle.Users().Create(ctx, "alice", "other@example.com", CreateUserOptions{Password: syntheticPassword})
	require.True(t, IsErrUserAlreadyExist(err), "duplicate username must be rejected, got %v", err)

	dupAccessErr := Handle.db.Create(&Access{UserID: bob.ID, RepoID: repo.ID, Mode: AccessModeRead}).Error
	require.Error(t, dupAccessErr, "duplicate (user_id, repo_id) access must violate the unique index")

	rollbackErr := Handle.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&Access{UserID: bob.ID, RepoID: repo2.ID, Mode: AccessModeWrite}).Error; err != nil {
			return err
		}
		return errors.New("intentional rollback")
	})
	require.Error(t, rollbackErr, "transaction must surface the intentional rollback")

	var rolledBack Access
	err = Handle.db.Where("user_id = ? AND repo_id = ?", bob.ID, repo2.ID).First(&rolledBack).Error
	require.True(t, errors.Is(err, gorm.ErrRecordNotFound), "rolled-back access row must not persist, got %v", err)

	t.Log("stage 5: close and reopen both pools, then re-run migration and Sync2")
	closePools(t)
	require.NoError(t, NewEngine(), "re-run NewEngine after closing pools")
	require.NotNil(t, Handle)
	require.NotNil(t, x)

	persistedAlice, err := Handle.Users().GetByUsername(ctx, "alice")
	require.NoError(t, err, "read alice after pool reopen")
	assert.Equal(t, alice.ID, persistedAlice.ID)

	t.Log("stage 6: restart both engine processes against the same pkvdb file")
	closePools(t)
	// Stop PizzaSQL before PizzaKV so the engine drops its KV connections cleanly.
	sql.stop()
	kv.stop()

	kvPort2 := freePort(t)
	pgPort2 := freePort(t)
	conf.Database.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(pgPort2))
	kv = startPizzaKV(t, kvBin, kvPort2, kvPath, logs)
	sql = startPizzaSQL(t, sqlBin, kvPort2, pgPort2, logs)
	require.NoError(t, NewEngine(), "NewEngine after full engine + KV restart")

	require.True(t, Handle.db.Migrator().HasTable(new(AccessToken)), "access_token table must persist across restart")
	repoTable, err = x.IsTableExist(new(Repository))
	require.NoError(t, err, "check repository table via xorm after restart")
	require.True(t, repoTable, "repository table must persist across restart")

	restartedAlice, err := Handle.Users().GetByUsername(ctx, "alice")
	require.NoError(t, err, "read alice after engine restart")
	assert.Equal(t, alice.ID, restartedAlice.ID)

	restartedRepo, err := Handle.Repositories().GetByName(ctx, alice.ID, "hello")
	require.NoError(t, err, "read repository after engine restart")
	assert.Equal(t, repo.ID, restartedRepo.ID)

	restartedTokens, err := Handle.AccessTokens().List(ctx, alice.ID)
	require.NoError(t, err, "list access tokens after engine restart")
	require.Len(t, restartedTokens, 1)
	assert.Equal(t, "smoke-token", restartedTokens[0].Name)

	var restartedAccess Access
	ok, err = x.Where("user_id = ? AND repo_id = ?", bob.ID, repo.ID).Get(&restartedAccess)
	require.NoError(t, err, "read access after engine restart")
	require.True(t, ok, "access row must survive engine restart")
	assert.Equal(t, AccessModeWrite, restartedAccess.Mode)

	// Safety: the only credential in play is the synthetic constant, and it must
	// never reach the engine logs even though it is embedded in the DSN.
	assert.Equal(t, syntheticPassword, conf.Database.Password)
	require.NotContains(t, logs.String(), syntheticPassword, "engine logs must never contain the synthetic credential")

	t.Log("stage 7: LFS object writes, duplicate rejection, and reads")
	require.NoError(t, Handle.LFS().CreateObject(ctx, repo.ID, smokeOIDAlpha, 1234, lfsutil.StorageLocal), "create LFS object alpha")
	require.NoError(t, Handle.LFS().CreateObject(ctx, repo.ID, smokeOIDBeta, 5678, lfsutil.StorageLocal), "create a second LFS object in the same repository")
	require.NoError(t, Handle.LFS().CreateObject(ctx, repo2.ID, smokeOIDAlpha, 9999, lfsutil.StorageLocal), "create the same LFS object in a different repository")

	require.Error(t, Handle.LFS().CreateObject(ctx, repo.ID, smokeOIDAlpha, 4321, lfsutil.StorageLocal), "duplicate (repo_id, oid) LFS object must be rejected")

	lfsAlpha, err := Handle.LFS().GetObjectByOID(ctx, repo.ID, smokeOIDAlpha)
	require.NoError(t, err, "read LFS object alpha via LFSStore")
	assert.Equal(t, int64(1234), lfsAlpha.Size)
	assert.Equal(t, lfsutil.StorageLocal, lfsAlpha.Storage)
	assert.False(t, lfsAlpha.CreatedAt.IsZero(), "LFS created_at must be a real time.Time")
	assert.WithinDuration(t, time.Now(), lfsAlpha.CreatedAt, 5*time.Minute, "LFS created_at must be near the write time")

	lfsBeta, err := Handle.LFS().GetObjectByOID(ctx, repo.ID, smokeOIDBeta)
	require.NoError(t, err, "read second LFS object in the same repository")
	assert.Equal(t, int64(5678), lfsBeta.Size)

	sameRepoObjects, err := Handle.LFS().GetObjectsByOIDs(ctx, repo.ID, smokeOIDAlpha, smokeOIDBeta)
	require.NoError(t, err, "read multiple LFS objects by OID")
	require.Len(t, sameRepoObjects, 2, "both objects in the same repository must be returned")

	lfsAcrossRepo, err := Handle.LFS().GetObjectByOID(ctx, repo2.ID, smokeOIDAlpha)
	require.NoError(t, err, "read the same OID from a different repository")
	assert.Equal(t, int64(9999), lfsAcrossRepo.Size, "same OID must keep a distinct size per repository")
}

// freePort reserves an ephemeral loopback port by binding :0 and releasing it.
func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// waitTCP polls until the named child accepts connections on the loopback port.
func waitTCP(t *testing.T, port int, timeout time.Duration, name string, logs *lockedBuffer) {
	t.Helper()
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 250*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s did not listen on %s within %s.\n%s", name, addr, timeout, tailLines(logs.String(), 200))
}

// startChild launches a child and registers its stop with t.Cleanup immediately
// so a readiness t.Fatal cannot leak the subprocess.
func startChild(t *testing.T, name string, argv []string, out *lockedBuffer) *childProcess {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = out
	cmd.Stderr = out
	require.NoError(t, cmd.Start(), "start %s", name)
	c := &childProcess{name: name, cmd: cmd}
	t.Cleanup(c.stop)
	return c
}

// startPizzaKV launches a disposable PizzaKV bound to a loopback port with its
// durable database inside the test temp dir.
func startPizzaKV(t *testing.T, bin string, port int, path string, logs *lockedBuffer) *childProcess {
	t.Helper()
	c := startChild(t, "pizzakv", []string{
		bin,
		"-host=127.0.0.1",
		"-port=" + strconv.Itoa(port),
		"-path=" + path,
	}, logs)
	waitTCP(t, port, 10*time.Second, "pizzakv", logs)
	return c
}

// startPizzaSQL launches a disposable PizzaSQL engine against the running
// PizzaKV and exposes the PostgreSQL wire protocol on a loopback port. "-quiet"
// is omitted so the engine logs each rejected query, which the failure dump
// replays with only synthetic test data.
func startPizzaSQL(t *testing.T, bin string, kvPort, pgPort int, logs *lockedBuffer) *childProcess {
	t.Helper()
	c := startChild(t, "pizzasql", []string{
		bin,
		"-y",
		"-kvaddr=127.0.0.1:" + strconv.Itoa(kvPort),
		"-db=gogs",
		"-pool=16",
		"-pg",
		"-pg-host=127.0.0.1",
		"-pg-port=" + strconv.Itoa(pgPort),
	}, logs)
	waitTCP(t, pgPort, 15*time.Second, "pizzasql", logs)
	return c
}

// configurePizzaSQL wires the global Gogs configuration to the disposable
// engine via a temp app.ini, mirroring the real startup path (conf.Init plus
// InitLogging).
func configurePizzaSQL(t *testing.T, dir string, pgPort int) {
	t.Helper()
	logRoot := filepath.Join(dir, "log")
	iniPath := filepath.Join(dir, "app.ini")
	content := fmt.Sprintf(`[server]
DISABLE_SSH = true

[database]
TYPE = pizzasql
HOST = 127.0.0.1:%d
NAME = gogs
USER = gogs
PASSWORD = %s
SSL_MODE = disable
MAX_OPEN_CONNS = 3
MAX_IDLE_CONNS = 3

[log]
ROOT_PATH = %s
`, pgPort, syntheticPassword, logRoot)
	require.NoError(t, os.WriteFile(iniPath, []byte(content), 0o600))
	require.NoError(t, conf.Init(iniPath), "load disposable app.ini")
	conf.InitLogging(true)
	require.NoError(t, os.MkdirAll(conf.Log.RootPath, 0o755), "create log root")
}

// closePools closes the shared xorm engine and GORM handle and nils the globals
// so a subsequent NewEngine reconnects from scratch.
func closePools(t *testing.T) {
	t.Helper()
	if Handle != nil {
		if sqlDB, err := Handle.db.DB(); err == nil {
			_ = sqlDB.Close()
		} else {
			t.Logf("get gorm *sql.DB: %v", err)
		}
		Handle = nil
	}
	if x != nil {
		if err := x.Close(); err != nil {
			t.Logf("close xorm engine: %v", err)
		}
		x = nil
	}
}
