package forgedb

import (
	"fmt"
	"path/filepath"
	"testing"
)

func BenchmarkInsert(b *testing.B) {
	db := benchDB(b)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := fmt.Sprintf(`INSERT INTO account VALUES (%d, 1)`, int64(i)+1)
		if _, err := db.Exec(q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSelect(b *testing.B) {
	db := benchDB(b)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		b.Fatal(err)
	}
	const rows = 32
	for i := 1; i <= rows; i++ {
		q := fmt.Sprintf(`INSERT INTO account VALUES (%d, %d)`, i, i)
		if _, err := db.Exec(q); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := (i % rows) + 1
		q := fmt.Sprintf(`SELECT balance FROM account WHERE id = %d`, id)
		res, err := db.Exec(q)
		if err != nil {
			b.Fatal(err)
		}
		if len(res.Rows) != 1 || res.Rows[0][0].Int != int64(id) {
			b.Fatalf("row %+v", res.Rows)
		}
	}
}

func BenchmarkUpdate(b *testing.B) {
	db := benchDB(b)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		b.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 0)`); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q := fmt.Sprintf(`UPDATE account SET balance = %d WHERE id = 1`, i+1)
		if _, err := db.Exec(q); err != nil {
			b.Fatal(err)
		}
	}
}

func benchDB(b *testing.B) *DB {
	b.Helper()
	db, err := Create(filepath.Join(b.TempDir(), "forge.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	return db
}
