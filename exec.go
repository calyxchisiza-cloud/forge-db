package forgedb

import (
	"strings"

	"forgedb/internal/sql"
)

// ErrSyntax means the statement is not in the supported grammar.
var ErrSyntax = sql.ErrSyntax

// Result is the outcome of one SQL statement.
// Rows is set for a query. Changed counts inserted, updated, or deleted rows.
type Result struct {
	Columns []string
	Rows    [][]Value
	Changed int
}

// Exec runs one statement. A comparison with NULL is not true.
// The statement supplies literal values; it does not evaluate expressions.
// A query shares the database with other queries. A change waits for them,
// and BEGIN keeps that write lock until COMMIT, ROLLBACK, or a failed statement.
func (db *DB) Exec(query string) (*Result, error) {
	switch sqlKind(query) {
	case kindBegin:
		return db.execBegin(query)
	case kindEnd:
		return db.execEnd(query)
	case kindWrite:
		return db.execLocked(query, true)
	default:
		return db.execLocked(query, false)
	}
}

func (db *DB) execBegin(query string) (*Result, error) {
	held := db.pool.Writing()
	if !held {
		db.pool.LockWrite()
	}
	if db.closed {
		if !held {
			db.pool.UnlockWrite()
		}
		return nil, ErrClosed
	}
	res, err := db.run(query)
	if err != nil && !held {
		db.pool.UnlockWrite()
	}
	return res, err
}

func (db *DB) execEnd(query string) (*Result, error) {
	held := db.pool.Writing()
	if !held {
		db.pool.LockWrite()
	}
	if db.closed {
		if !held {
			db.pool.UnlockWrite()
		}
		return nil, ErrClosed
	}
	res, err := db.run(query)
	if err != nil {
		if !held {
			db.pool.UnlockWrite()
		}
		return nil, err
	}
	db.pool.UnlockWrite()
	return res, nil
}

func (db *DB) execLocked(query string, write bool) (*Result, error) {
	held := db.pool.Writing()
	if !held {
		if write {
			db.pool.LockWrite()
			defer db.pool.UnlockWrite()
		} else {
			db.pool.LockRead()
			defer db.pool.UnlockRead()
		}
	}
	if db.closed {
		return nil, ErrClosed
	}
	res, err := db.run(query)
	if err != nil && held && !db.pool.Active() {
		db.pool.UnlockWrite()
	}
	return res, err
}

func (db *DB) run(query string) (*Result, error) {
	res, err := db.eng.Exec(query)
	if err != nil {
		return nil, err
	}
	out := &Result{
		Columns: append([]string(nil), res.Columns...),
		Changed: res.Changed,
		Rows:    make([][]Value, len(res.Rows)),
	}
	for i, row := range res.Rows {
		out.Rows[i] = fromStorageValues(row)
	}
	return out, nil
}

const (
	kindRead = iota
	kindWrite
	kindBegin
	kindEnd
)

func sqlKind(src string) int {
	fields := strings.Fields(src)
	if len(fields) == 0 {
		return kindRead
	}
	switch strings.ToUpper(fields[0]) {
	case "BEGIN":
		return kindBegin
	case "COMMIT", "ROLLBACK":
		return kindEnd
	case "INSERT", "UPDATE", "DELETE", "CREATE":
		return kindWrite
	default:
		return kindRead
	}
}
