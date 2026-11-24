package forgedb

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestPublicExecRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	db, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 50)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE account SET balance = 40 WHERE id = 1`); err != nil {
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
	res, err := db.Exec(`SELECT balance FROM account WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Rows) != 1 || res.Rows[0][0].Int != 40 {
		t.Fatalf("row %+v", res.Rows)
	}
	if _, err := db.Exec(`SELECT`); !errors.Is(err, ErrSyntax) {
		t.Fatalf("syntax %v", err)
	}
}
