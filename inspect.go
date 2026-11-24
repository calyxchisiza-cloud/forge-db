package forgedb

import (
	"os"

	"forgedb/internal/sql"
)

const inspectPageLimit = 512

// TableView is one table in the schema catalog.
type TableView struct {
	Name       string
	Header     uint32
	Index      uint32
	PrimaryKey string
	Columns    []ColumnView
}

// ColumnView is one column. Primary is true for the table's primary key.
type ColumnView struct {
	Name    string
	Type    string
	Primary bool
}

// PageView is one page in file order.
type PageView struct {
	ID   uint32
	Kind string
	LSN  uint64
}

// Snapshot is a read of the open file: the header, the schema, and the page list.
type Snapshot struct {
	Path      string
	PageSize  int
	PageCount uint32
	FreeHead  uint32
	NextLSN   uint64
	Catalog   uint32
	WALBytes  int64
	Tables    []TableView
	Pages     []PageView
	Truncated bool
}

// Inspect reads the file header, schema, and page kinds. It does not start a transaction.
func (db *DB) Inspect() (*Snapshot, error) {
	unlock := db.pool.Guard(false)
	defer unlock()
	if db.closed {
		return nil, ErrClosed
	}
	snap := &Snapshot{
		Path:      db.p.Path(),
		PageSize:  PageSize,
		PageCount: db.p.PageCount(),
		FreeHead:  db.p.FreeHead(),
		NextLSN:   db.p.NextLSN(),
		Catalog:   db.p.Catalog(),
		Tables:    tableViews(db.eng.Tables()),
	}
	info, err := os.Stat(snap.Path + ".wal")
	if err == nil {
		snap.WALBytes = info.Size()
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	limit := snap.PageCount
	if limit > inspectPageLimit {
		limit = inspectPageLimit
		snap.Truncated = true
	}
	snap.Pages = make([]PageView, 0, limit)
	for id := uint32(0); id < limit; id++ {
		pg, err := db.pool.Fetch(id)
		if err != nil {
			return nil, err
		}
		snap.Pages = append(snap.Pages, PageView{ID: pg.ID(), Kind: pg.Kind(), LSN: pg.LSN()})
		if err := db.pool.Unpin(id); err != nil {
			return nil, err
		}
	}
	return snap, nil
}

func tableViews(tables []sql.TableInfo) []TableView {
	out := make([]TableView, len(tables))
	for i, table := range tables {
		cols := make([]ColumnView, len(table.Columns))
		for j, col := range table.Columns {
			cols[j] = ColumnView{Name: col.Name, Type: col.Type, Primary: col.Primary}
		}
		out[i] = TableView{
			Name:       table.Name,
			Header:     table.Header,
			Index:      table.Index,
			PrimaryKey: table.PrimaryKey,
			Columns:    cols,
		}
	}
	return out
}
