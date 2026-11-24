package sql

import (
	"errors"
	"path/filepath"
	"testing"

	"forgedb/internal/storage"
)

func TestStatementsAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	eng, pool, pager := newEngine(t, path)
	mustExec(t, eng, `CREATE TABLE account (
		id INTEGER PRIMARY KEY,
		name TEXT,
		balance INTEGER
	)`)
	if _, err := eng.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY)`); !errors.Is(err, storage.ErrSchema) {
		t.Fatalf("duplicate table %v", err)
	}
	res := mustExec(t, eng, `INSERT INTO account VALUES (1, 'Ada', 50)`)
	if res.Changed != 1 {
		t.Fatalf("changed %d", res.Changed)
	}
	mustExec(t, eng, `INSERT INTO account VALUES (2, 'Bea', NULL)`)
	if _, err := eng.Exec(`INSERT INTO account VALUES (1, 'No', 1)`); !errors.Is(err, storage.ErrDuplicate) {
		t.Fatalf("duplicate key %v", err)
	}
	if _, err := eng.Exec(`INSERT INTO account VALUES (3, 'No')`); !errors.Is(err, storage.ErrSchema) {
		t.Fatalf("arity %v", err)
	}
	res = mustExec(t, eng, `SELECT name, balance FROM account WHERE id = 1`)
	if len(res.Rows) != 1 || res.Rows[0][0].Text == nil || string(res.Rows[0][0].Text) != "Ada" || res.Rows[0][1].Int != 50 {
		t.Fatalf("row %+v", res.Rows)
	}
	res = mustExec(t, eng, `SELECT id FROM account WHERE balance = NULL`)
	if len(res.Rows) != 0 {
		t.Fatalf("null comparison %+v", res.Rows)
	}
	res = mustExec(t, eng, `SELECT name FROM account WHERE name > 'Ada' ORDER BY name LIMIT 1`)
	if len(res.Rows) != 1 || string(res.Rows[0][0].Text) != "Bea" {
		t.Fatalf("order %+v", res.Rows)
	}
	res = mustExec(t, eng, `UPDATE account SET balance = 40 WHERE id = 1`)
	if res.Changed != 1 {
		t.Fatalf("update %d", res.Changed)
	}
	res = mustExec(t, eng, `UPDATE account SET balance = 60 WHERE id = 2`)
	if res.Changed != 1 {
		t.Fatalf("update null %d", res.Changed)
	}
	res = mustExec(t, eng, `DELETE FROM account WHERE name = 'Bea'`)
	if res.Changed != 1 {
		t.Fatalf("delete %d", res.Changed)
	}
	if _, err := eng.Exec(`SELECT * FROM account WHERE balance = '40'`); !errors.Is(err, storage.ErrSchema) {
		t.Fatalf("type %v", err)
	}
	if err := pool.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := pager.Close(); err != nil {
		t.Fatal(err)
	}
	pager, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer pager.Close()
	pool, err = storage.NewPool(pager, 8)
	if err != nil {
		t.Fatal(err)
	}
	eng, err = Open(pool, pager)
	if err != nil {
		t.Fatal(err)
	}
	res = mustExec(t, eng, `SELECT id, name, balance FROM account ORDER BY id`)
	if len(res.Rows) != 1 || res.Rows[0][0].Int != 1 || string(res.Rows[0][1].Text) != "Ada" || res.Rows[0][2].Int != 40 {
		t.Fatalf("reopen %+v", res.Rows)
	}
	if _, err := eng.Exec(`SELECT id`); !errors.Is(err, ErrSyntax) {
		t.Fatalf("syntax %v", err)
	}
}

func TestQuotedTextAndMinInteger(t *testing.T) {
	eng, _, _ := newEngine(t, filepath.Join(t.TempDir(), "forge.db"))
	mustExec(t, eng, `CREATE TABLE note (id INTEGER PRIMARY KEY, body TEXT)`)
	mustExec(t, eng, `INSERT INTO note VALUES (-9223372036854775808, 'a''b')`)
	res := mustExec(t, eng, `SELECT body FROM note WHERE id = -9223372036854775808`)
	if len(res.Rows) != 1 || string(res.Rows[0][0].Text) != "a'b" {
		t.Fatalf("row %+v", res.Rows)
	}
}

func newEngine(t *testing.T, path string) (*Engine, *storage.Pool, *storage.Pager) {
	t.Helper()
	pager, err := storage.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pager.Close() })
	pool, err := storage.NewPool(pager, 8)
	if err != nil {
		t.Fatal(err)
	}
	eng, err := Open(pool, pager)
	if err != nil {
		t.Fatal(err)
	}
	return eng, pool, pager
}

func mustExec(t *testing.T, eng *Engine, query string) *Result {
	t.Helper()
	res, err := eng.Exec(query)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return res
}
