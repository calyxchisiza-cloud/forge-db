package forgedb

import "forgedb/internal/storage"

var (
	// ErrDuplicate means the index already stores this key.
	ErrDuplicate = storage.ErrDuplicate

	// ErrKeyTooLarge means the key cannot be stored in an index page.
	ErrKeyTooLarge = storage.ErrKeyTooLarge
)

// Index maps a byte key to a row. Keys are ordered, and a key is stored once.
type Index struct {
	inner *storage.Index
}

// CreateIndex stores an empty B+Tree.
func (db *DB) CreateIndex() (*Index, error) {
	unlock := db.pool.Guard(true)
	defer unlock()
	if db.closed {
		return nil, ErrClosed
	}
	inner, err := storage.CreateIndex(db.pool)
	if err != nil {
		return nil, err
	}
	return &Index{inner: inner}, nil
}

// OpenIndex loads an index from the catalog page written by CreateIndex.
func (db *DB) OpenIndex(meta uint32) (*Index, error) {
	unlock := db.pool.Guard(false)
	defer unlock()
	if db.closed {
		return nil, ErrClosed
	}
	inner, err := storage.OpenIndex(db.pool, meta)
	if err != nil {
		return nil, err
	}
	return &Index{inner: inner}, nil
}

// MetaID is the catalog page. It does not change when the root splits.
func (ix *Index) MetaID() uint32 { return ix.inner.MetaID() }

// Put inserts a key. The key is copied.
func (ix *Index) Put(key []byte, id RowID) error {
	defer ix.inner.Guard(true)()
	return ix.inner.Put(key, storage.RowID{Page: id.Page, Slot: id.Slot})
}

// Get returns the row stored for key.
func (ix *Index) Get(key []byte) (RowID, error) {
	defer ix.inner.Guard(false)()
	id, err := ix.inner.Get(key)
	return RowID{Page: id.Page, Slot: id.Slot}, err
}

// Delete removes a key.
func (ix *Index) Delete(key []byte) error {
	defer ix.inner.Guard(true)()
	return ix.inner.Delete(key)
}

// Scan visits keys from smallest to largest.
func (ix *Index) Scan(fn func([]byte, RowID) error) error {
	defer ix.inner.Guard(false)()
	return ix.inner.Scan(func(key []byte, id storage.RowID) error {
		return fn(key, RowID{Page: id.Page, Slot: id.Slot})
	})
}

// Check rejects a broken parent pointer, separator, key order, or leaf link.
func (ix *Index) Check() error {
	defer ix.inner.Guard(false)()
	return ix.inner.Check()
}
