package forgedb

import "forgedb/internal/sql"

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
func (db *DB) Exec(query string) (*Result, error) {
	if db.closed {
		return nil, ErrClosed
	}
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
