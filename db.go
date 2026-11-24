// Package forgedb is a single-file relational database.
// Phase 1 stores fixed-size pages. Records, indexes, and SQL come later.
package forgedb

import "forgedb/internal/storage"

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
)

// PageSize is the fixed size of every database page.
const PageSize = storage.PageSize

// DB is an open database file.
type DB struct {
	p *storage.Pager
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
	return &DB{p: p}, nil
}

// Open reads an existing database file and checks its header.
func Open(path string) (*DB, error) {
	p, err := storage.Open(path)
	if err != nil {
		return nil, err
	}
	return &DB{p: p}, nil
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
func (db *DB) Read(id uint32) (*Page, error) {
	pg, err := db.p.Read(id)
	if err != nil {
		return nil, err
	}
	return &Page{inner: pg}, nil
}

// Write stores a data page.
func (db *DB) Write(pg *Page) error {
	return db.p.Write(pg.inner)
}

// Free returns a data page to the free list. Page 0 cannot be freed.
func (db *DB) Free(id uint32) error {
	return db.p.Free(id)
}

// Flush pushes file data to durable storage.
func (db *DB) Flush() error {
	return db.p.Flush()
}

// Close flushes the file and releases it.
func (db *DB) Close() error {
	return db.p.Close()
}

// ID returns the page identifier.
func (pg *Page) ID() uint32 {
	return pg.inner.ID()
}

// Payload returns the writable bytes after the page header.
func (pg *Page) Payload() []byte {
	return pg.inner.Payload()
}
