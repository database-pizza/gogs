package database

import (
	"time"

	"gogs.io/gogs/internal/lfsutil"
)

// pizzaLFSObject is the PizzaSQL-only table shape for the "lfs_object" table.
// The PizzaSQL engine collapses a composite primary key to its first column, so
// the shared (repo_id, oid) primary key of LFSObject would collapse to repo_id
// and lose uniqueness. A surrogate auto-increment ID paired with a unique index
// keeps the same relation semantics. The application keeps querying through the
// ID-less LFSObject model.
type pizzaLFSObject struct {
	ID        int64           `gorm:"primaryKey"`
	RepoID    int64           `gorm:"not null;uniqueIndex:idx_lfs_object_repo_oid,priority:1"`
	OID       lfsutil.OID     `gorm:"column:oid;not null;uniqueIndex:idx_lfs_object_repo_oid,priority:2"`
	Size      int64           `gorm:"not null"`
	Storage   lfsutil.Storage `gorm:"not null"`
	CreatedAt time.Time       `gorm:"not null"`
}

func (pizzaLFSObject) TableName() string {
	return "lfs_object"
}
