package forgedb

import "forgedb/internal/storage"

// Type is the kind of value stored in a column.
type Type uint8

const (
	// Integer is a signed 64-bit integer.
	Integer Type = Type(storage.TypeInteger)
	// Text is a byte string.
	Text Type = Type(storage.TypeText)
)

var (
	// ErrNotFound means the row is missing.
	ErrNotFound = storage.ErrNotFound

	// ErrRowTooLarge means the encoded row does not fit in one page.
	ErrRowTooLarge = storage.ErrRowTooLarge

	// ErrSchema means the columns or values do not match the table.
	ErrSchema = storage.ErrSchema

	// ErrPageType means a data write targeted a table or slotted page.
	ErrPageType = storage.ErrPageType
)

// Column is one field in a table.
type Column struct {
	Name string
	Type Type
}

// Value is one column value. A null value ignores Int and Text.
type Value struct {
	Null bool
	Int  int64
	Text []byte
}

// RowID locates a live row.
type RowID struct {
	Page uint32
	Slot uint16
}

// Table stores rows for one column list.
type Table struct {
	inner *storage.Table
}

// CreateTable stores the column list and returns an empty table.
func (db *DB) CreateTable(cols []Column) (*Table, error) {
	inner, err := storage.CreateTable(db.p, toStorageColumns(cols))
	if err != nil {
		return nil, err
	}
	return &Table{inner: inner}, nil
}

// OpenTable loads a table from the header page written by CreateTable.
func (db *DB) OpenTable(header uint32) (*Table, error) {
	inner, err := storage.OpenTable(db.p, header)
	if err != nil {
		return nil, err
	}
	return &Table{inner: inner}, nil
}

// HeaderID is the page that stores this table's columns.
func (t *Table) HeaderID() uint32 { return t.inner.HeaderID() }

// Insert adds a row.
func (t *Table) Insert(vals []Value) (RowID, error) {
	id, err := t.inner.Insert(toStorageValues(vals))
	return RowID{Page: id.Page, Slot: id.Slot}, err
}

// Get reads one row.
func (t *Table) Get(id RowID) ([]Value, error) {
	vals, err := t.inner.Get(storage.RowID{Page: id.Page, Slot: id.Slot})
	return fromStorageValues(vals), err
}

// Update replaces a row. The identifier is unchanged when the new row fits on the same page.
func (t *Table) Update(id RowID, vals []Value) (RowID, error) {
	next, err := t.inner.Update(storage.RowID{Page: id.Page, Slot: id.Slot}, toStorageValues(vals))
	return RowID{Page: next.Page, Slot: next.Slot}, err
}

// Delete removes a row.
func (t *Table) Delete(id RowID) error {
	return t.inner.Delete(storage.RowID{Page: id.Page, Slot: id.Slot})
}

// Scan visits live rows in page order.
func (t *Table) Scan(fn func(RowID, []Value) error) error {
	return t.inner.Scan(func(id storage.RowID, vals []storage.Value) error {
		return fn(RowID{Page: id.Page, Slot: id.Slot}, fromStorageValues(vals))
	})
}

func toStorageColumns(cols []Column) []storage.Column {
	out := make([]storage.Column, len(cols))
	for i, col := range cols {
		out[i] = storage.Column{Name: col.Name, Type: storage.Type(col.Type)}
	}
	return out
}

func toStorageValues(vals []Value) []storage.Value {
	out := make([]storage.Value, len(vals))
	for i, val := range vals {
		out[i] = storage.Value{Null: val.Null, Int: val.Int, Text: val.Text}
	}
	return out
}

func fromStorageValues(vals []storage.Value) []Value {
	if vals == nil {
		return nil
	}
	out := make([]Value, len(vals))
	for i, val := range vals {
		out[i] = Value{Null: val.Null, Int: val.Int, Text: val.Text}
	}
	return out
}
