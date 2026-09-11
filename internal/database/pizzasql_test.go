package database

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func newPizzaSQLTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		NamingStrategy: schema.NamingStrategy{SingularTable: true},
	})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(new(pizzaLFSObject)))
	return db
}

func TestPizzaLFSObjectSchema(t *testing.T) {
	db := newPizzaSQLTestDB(t)

	type columnInfo struct {
		Name string `gorm:"column:name"`
		PK   int    `gorm:"column:pk"`
	}
	var columns []columnInfo
	err := db.Raw("PRAGMA table_info(lfs_object)").Scan(&columns).Error
	require.NoError(t, err)

	names := make([]string, 0, len(columns))
	pkByColumn := make(map[string]int, len(columns))
	for _, col := range columns {
		names = append(names, col.Name)
		pkByColumn[col.Name] = col.PK
	}
	assert.ElementsMatch(t, []string{"id", "repo_id", "oid", "size", "storage", "created_at"}, names)
	assert.Equal(t, 1, pkByColumn["id"], "id must be the sole primary key")
	assert.Equal(t, 0, pkByColumn["repo_id"], "repo_id must not be part of the primary key")
	assert.Equal(t, 0, pkByColumn["oid"], "oid must not be part of the primary key")

	require.True(t, db.Migrator().HasIndex("lfs_object", "idx_lfs_object_repo_oid"))

	type indexInfo struct {
		Name   string `gorm:"column:name"`
		Unique int    `gorm:"column:unique"`
	}
	var indexes []indexInfo
	err = db.Raw("PRAGMA index_list(lfs_object)").Scan(&indexes).Error
	require.NoError(t, err)

	var found bool
	for _, idx := range indexes {
		if idx.Name == "idx_lfs_object_repo_oid" {
			found = true
			assert.Equal(t, 1, idx.Unique, "index must be unique")
		}
	}
	assert.True(t, found, "unique index idx_lfs_object_repo_oid must exist")

	type indexColumn struct {
		Name string `gorm:"column:name"`
	}
	var indexColumns []indexColumn
	err = db.Raw("PRAGMA index_info(idx_lfs_object_repo_oid)").Scan(&indexColumns).Error
	require.NoError(t, err)
	require.Len(t, indexColumns, 2)
	assert.Equal(t, "repo_id", indexColumns[0].Name)
	assert.Equal(t, "oid", indexColumns[1].Name)
}
