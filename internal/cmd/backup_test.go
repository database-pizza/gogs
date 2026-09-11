package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/conf"
)

func TestCheckPizzaSQLBackupSupport(t *testing.T) {
	prev := conf.Database.Type
	t.Cleanup(func() { conf.Database.Type = prev })

	for _, dbType := range []string{"sqlite3", "mysql", "postgres", "mssql"} {
		conf.Database.Type = dbType
		require.NoError(t, checkPizzaSQLBackupSupport(), "backend %q must be supported", dbType)
	}

	conf.Database.Type = "pizzasql"
	err := checkPizzaSQLBackupSupport()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "pizzasql")
	assert.Contains(t, err.Error(), "lfs_object")
	assert.Contains(t, err.Error(), "separately")
}
