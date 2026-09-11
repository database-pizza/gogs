package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadPasswordFile(t *testing.T) {
	t.Run("missing file", func(t *testing.T) {
		_, err := readPasswordFile(filepath.Join(t.TempDir(), "nope"))
		require.Error(t, err)
	})

	t.Run("directory", func(t *testing.T) {
		_, err := readPasswordFile(t.TempDir())
		require.Error(t, err)
	})

	t.Run("group readable", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		require.NoError(t, os.WriteFile(path, []byte("secret\n"), 0o600))
		require.NoError(t, os.Chmod(path, 0o640))

		_, err := readPasswordFile(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permissions")
	})

	t.Run("empty", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		require.NoError(t, os.WriteFile(path, []byte("\n"), 0o600))

		_, err := readPasswordFile(path)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty")
	})

	t.Run("trims only trailing newlines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		require.NoError(t, os.WriteFile(path, []byte(" secret \n\n"), 0o600))

		password, err := readPasswordFile(path)
		require.NoError(t, err)
		assert.Equal(t, " secret ", password)
	})
}

func TestResolvePassword(t *testing.T) {
	t.Run("neither flag", func(t *testing.T) {
		_, err := resolvePassword("", "", false, false)
		require.Error(t, err)
	})

	t.Run("both flags", func(t *testing.T) {
		_, err := resolvePassword("secret", "/tmp/password", true, true)
		require.Error(t, err)
	})

	t.Run("inline password", func(t *testing.T) {
		password, err := resolvePassword("secret", "", true, false)
		require.NoError(t, err)
		assert.Equal(t, "secret", password)
	})

	t.Run("password file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		require.NoError(t, os.WriteFile(path, []byte("secret\n"), 0o600))

		password, err := resolvePassword("", path, false, true)
		require.NoError(t, err)
		assert.Equal(t, "secret", password)
	})

	t.Run("password file with insecure permissions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "password")
		require.NoError(t, os.WriteFile(path, []byte("secret\n"), 0o600))
		require.NoError(t, os.Chmod(path, 0o644))

		_, err := resolvePassword("", path, false, true)
		require.Error(t, err)
	})
}
