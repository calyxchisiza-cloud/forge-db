// Package forgedb is a single-file relational database.
package forgedb

import (
	"forgedb/internal/sql"
	"forgedb/internal/storage"
)

var (
	// ErrCorrupt means the file, a page checksum, or the free list is not valid.
	ErrCorrupt = storage.ErrCorrupt

	// ErrPageIDMismatch means a page's stored identifier does not match its position.
	ErrPageIDMismatch = storage.ErrPageIDMismatch

	// ErrPageRange means the page identifier is outside the file.
	ErrPageRange = storage.ErrPageRange

	// ErrFreeFileHeader means page 0 was passed to Free.
	ErrFreeFileHeader = storage.ErrFreeFileHeader

	// ErrDoubleFree means Free was called for a page that is already free.
	ErrDoubleFree = storage.ErrDoubleFree

	// ErrFreedPage means Write was called for a page on the free list.
	ErrFreedPage = storage.ErrFreedPage

	// ErrClosed means the database has been closed.
	ErrClosed = storage.ErrClosed

	// ErrNoTxn means commit or rollback was used with no transaction.
	ErrNoTxn = storage.ErrNoTxn

	// ErrTxnOpen means a transaction is already open.
	ErrTxnOpen = storage.ErrTxnOpen
)

// PageSize is the fixed size of every database page.
const PageSize = storage.PageSize

// DB is an open database file. Page traffic goes through a fixed-size buffer pool.
type DB struct {
	p      *storage.Pager
	pool   *storage.Pool
	eng    *sql.Engine
	closed bool
}

// Page is one database page. Payload is the bytes after the page header.
type Page struct {
	inner *storage.Page
}

// Create makes a new database file. The file must not already exist.
func Create(path string) (*DB, error) {
	p, err := storage.Create(path)
	if err != nil {
		return nil, err
	}
	return newDB(p)
}

// Open reads an existing database file and checks its header.
func Open(path string) (*DB, error) {
	p, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	return newDB(p)
}

func newDB(p *storage.Pager) (*DB, error) {
	pool, err := storage.NewPool(p, 64)
	if err != nil {
		p.Close()
		return nil, err
	}
	eng, err := sql.Open(pool, p)
	if err != nil {
		p.Close()
		return nil, err
	}
	return &DB{p: p, pool: pool, eng: eng}, nil
}

// Allocate reserves a data page. A free page is reused before the file grows.
func (db *DB) Allocate() (*Page, error) {
	pg, err := db.p.Allocate()
	if err != nil {
		return nil, err
	}
	return &Page{inner: pg}, nil
}

// Read loads a page and checks its checksum and identifier.
// The returned page is a copy, so edits are stored only by Write.
func (db *DB) Read(id uint32) (*Page, error) {
	pg, err := db.pool.Fetch(id)
	if err != nil {
		return nil, err
	}
	defer db.pool.Unpin(id)
	return &Page{inner: storage.ClonePage(pg)}, nil
}

// Write stores a data page in the buffer pool. Close or Flush writes it to the file.
func (db *DB) Write(pg *Page) error {
	return db.pool.Store(pg.inner)
}

// Free returns a data page to the free list. Page 0 cannot be freed.
func (db *DB) Free(id uint32) error {
	return db.p.Free(id)
}

// Flush writes dirty pages and then syncs the file.
func (db *DB) Flush() error {
	return db.pool.Flush()
}

// Begin starts a transaction that stays open until Commit or Rollback.
func (db *DB) Begin() error {
	if db.closed {
		return ErrClosed
	}
	return db.pool.BeginExplicit()
}

// Commit writes the transaction to the log and syncs the log.
func (db *DB) Commit() error {
	if db.closed {
		return ErrClosed
	}
	return db.pool.Commit()
}

// Rollback discards the open transaction.
func (db *DB) Rollback() error {
	if db.closed {
		return ErrClosed
	}
	if err := db.pool.Rollback(); err != nil {
		return err
	}
	return db.eng.Reload()
}

// Checkpoint writes committed pages into the database file and clears the log.
func (db *DB) Checkpoint() error {
	if db.closed {
		return ErrClosed
	}
	return db.pool.Checkpoint()
}

// Close writes committed pages, clears the log, and releases the file.
// An open transaction is discarded.
func (db *DB) Close() error {
	if db.closed {
		return nil
	}
	db.closed = true
	if db.pool.Active() {
		_ = db.pool.Rollback()
	}
	checkErr := db.pool.Checkpoint()
	closeErr := db.p.Close()
	if checkErr != nil {
		return checkErr
	}
	return closeErr
}

// ID returns the page identifier.
func (pg *Page) ID() uint32 {
	return pg.inner.ID()
}

// Payload returns the writable bytes after the page header.
func (pg *Page) Payload() []byte {
	return pg.inner.Payload()
}
