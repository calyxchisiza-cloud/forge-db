package forgedb

import (
	"path/filepath"
	"testing"
)

func TestInspectShowsTablesAndPages(t *testing.T) {
	db, err := Create(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	empty, err := db.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if empty.PageCount != 1 || len(empty.Pages) != 1 || empty.Pages[0].Kind != "header" {
		t.Fatalf("empty %+v", empty)
	}
	if len(empty.Tables) != 0 {
		t.Fatalf("tables %+v", empty.Tables)
	}
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 'Ada')`); err != nil {
		t.Fatal(err)
	}
	snap, err := db.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Tables) != 1 || snap.Tables[0].Name != "account" || snap.Tables[0].PrimaryKey != "id" {
		t.Fatalf("tables %+v", snap.Tables)
	}
	if snap.Tables[0].Columns[1].Type != "TEXT" {
		t.Fatalf("columns %+v", snap.Tables[0].Columns)
	}
	seen := map[string]bool{}
	for _, pg := range snap.Pages {
		seen[pg.Kind] = true
	}
	for _, kind := range []string{"header", "catalog", "table", "index", "slotted"} {
		if !seen[kind] {
			t.Fatalf("missing %s in %+v", kind, snap.Pages)
		}
	}
	if snap.WALBytes == 0 {
		t.Fatal("log is empty")
	}
}
