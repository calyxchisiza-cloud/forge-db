package storage

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"
)

func TestRowsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	table, err := CreateTable(pool, []Column{{Name: "id", Type: TypeInteger}, {Name: "name", Type: TypeText}})
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := p.Write(mustRead(t, p, table.HeaderID())); !errors.Is(err, ErrPageType) {
		t.Fatalf("write header %v", err)
	}
	first, err := table.Insert([]Value{{Int: 1}, {Text: []byte("Calyx")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Insert([]Value{{Int: -42}, {Text: []byte{0, 255}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Insert([]Value{{Null: true}, {Null: true}}); err != nil {
		t.Fatal(err)
	}
	if err := pool.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pool, err = NewPool(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	table, err = OpenTable(pool, table.HeaderID())
	if err != nil {
		t.Fatal(err)
	}
	got, err := table.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Int != 1 || string(got[1].Text) != "Calyx" {
		t.Fatalf("row %+v", got)
	}
	var rows [][]Value
	if err := table.Scan(func(_ RowID, vals []Value) error {
		rows = append(rows, vals)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[1][0].Int != -42 || !bytes.Equal(rows[1][1].Text, []byte{0, 255}) {
		t.Fatalf("rows %+v", rows)
	}
	if !rows[2][0].Null || !rows[2][1].Null {
		t.Fatal("nulls were stored as values")
	}
}

func TestDeleteReusesTheSlot(t *testing.T) {
	pool := newTestPool(t)
	table, err := CreateTable(pool, []Column{{Name: "name", Type: TypeText}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := table.Insert([]Value{{Text: []byte("A")}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Insert([]Value{{Text: []byte("B")}}); err != nil {
		t.Fatal(err)
	}
	if err := table.Delete(first); err != nil {
		t.Fatal(err)
	}
	if _, err := table.Get(first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get %v", err)
	}
	reused, err := table.Insert([]Value{{Text: []byte("C")}})
	if err != nil {
		t.Fatal(err)
	}
	if reused != first {
		t.Fatalf("reused %#v want %#v", reused, first)
	}
	var names []string
	if err := table.Scan(func(_ RowID, vals []Value) error {
		names = append(names, string(vals[0].Text))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "C" || names[1] != "B" {
		t.Fatalf("names %v", names)
	}
}

func TestUpdateStaysOrMoves(t *testing.T) {
	pool := newTestPool(t)
	table, err := CreateTable(pool, []Column{{Name: "body", Type: TypeText}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := table.Insert([]Value{{Text: []byte("short")}})
	if err != nil {
		t.Fatal(err)
	}
	stayed, err := table.Update(id, []Value{{Text: []byte("longer")}})
	if err != nil {
		t.Fatal(err)
	}
	if stayed != id {
		t.Fatalf("update moved a row that fit: %#v", stayed)
	}
	big := bytes.Repeat([]byte("x"), 2000)
	first, err := table.Insert([]Value{{Text: big}})
	if err != nil {
		t.Fatal(err)
	}
	second, err := table.Insert([]Value{{Text: big}})
	if err != nil {
		t.Fatal(err)
	}
	grown := bytes.Repeat([]byte("y"), 3000)
	moved, err := table.Update(first, []Value{{Text: grown}})
	if err != nil {
		t.Fatal(err)
	}
	if moved == first {
		t.Fatal("growing row stayed on a full page")
	}
	if _, err := table.Get(first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old id %v", err)
	}
	got, err := table.Get(moved)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got[0].Text, grown) {
		t.Fatal("moved value mismatch")
	}
	got, err = table.Get(second)
	if err != nil || !bytes.Equal(got[0].Text, big) {
		t.Fatal("neighbor changed")
	}
}

func TestRowLargerThanAPageIsRejected(t *testing.T) {
	pool := newTestPool(t)
	before := pool.pager.PageCount()
	table, err := CreateTable(pool, []Column{{Name: "body", Type: TypeText}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = table.Insert([]Value{{Text: bytes.Repeat([]byte("z"), PageSize)}})
	if !errors.Is(err, ErrRowTooLarge) {
		t.Fatalf("error %v", err)
	}
	n := 0
	if err := table.Scan(func(RowID, []Value) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 0 || pool.pager.PageCount() != before+1 {
		t.Fatalf("rows %d pages %d before %d", n, pool.pager.PageCount(), before)
	}
}

func TestSchemaValidation(t *testing.T) {
	pool := newTestPool(t)
	if _, err := CreateTable(pool, nil); !errors.Is(err, ErrSchema) {
		t.Fatalf("empty %v", err)
	}
	cols := []Column{{Name: "id", Type: TypeInteger}, {Name: "id", Type: TypeInteger}}
	if _, err := CreateTable(pool, cols); !errors.Is(err, ErrSchema) {
		t.Fatalf("duplicate %v", err)
	}
	table, err := CreateTable(pool, []Column{{Name: "id", Type: TypeInteger}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := table.Insert(nil); !errors.Is(err, ErrSchema) {
		t.Fatalf("values %v", err)
	}
}

func newTestPool(t *testing.T) *Pool {
	t.Helper()
	p := newTestPager(t)
	pool, err := NewPool(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func newTestPager(t *testing.T) *Pager {
	t.Helper()
	p, err := Create(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func mustRead(t *testing.T, p *Pager, id uint32) *Page {
	t.Helper()
	pg, err := p.Read(id)
	if err != nil {
		t.Fatal(err)
	}
	return pg
}
