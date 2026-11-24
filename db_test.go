package forgedb

import (
	"bytes"
	"path/filepath"
	"testing"
)

func TestPublicCreateOpenRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	db, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	page, err := db.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(page.Payload(), []byte("Hello ForgeDB"))
	if err := db.Write(page); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	page, err = db.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(page.Payload(), []byte("Hello ForgeDB")) {
		t.Fatalf("payload %q", page.Payload()[:13])
	}
}
