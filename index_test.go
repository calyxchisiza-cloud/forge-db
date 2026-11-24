package forgedb

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPublicIndexRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	db, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := db.CreateIndex()
	if err != nil {
		t.Fatal(err)
	}
	if err := ix.Put([]byte("ada"), RowID{Page: 3, Slot: 1}); err != nil {
		t.Fatal(err)
	}
	meta := ix.MetaID()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ix, err = db.OpenIndex(meta)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ix.Get([]byte("ada"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != 3 || got.Slot != 1 {
		t.Fatalf("row %+v", got)
	}
	if err := ix.Delete([]byte("ada")); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Get([]byte("ada")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	var seen []byte
	if err := ix.Scan(func(key []byte, _ RowID) error {
		seen = append(seen, key...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 0 {
		t.Fatalf("keys %q", seen)
	}
}
