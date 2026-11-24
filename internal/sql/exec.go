package sql

import (
	"bytes"
	"errors"
	"sort"

	"forgedb/internal/storage"
)

// ErrSyntax means the statement is not in the supported grammar.
var ErrSyntax = errors.New("sql syntax error")

// Result is the outcome of one statement.
type Result struct {
	Columns []string
	Rows    [][]storage.Value
	Changed int
}

// Engine runs one statement at a time against a database.
type Engine struct {
	pool    *storage.Pool
	pager   *storage.Pager
	tables  map[string]*relation
	entries []storage.CatalogEntry
	catalog uint32
}

type relation struct {
	entry storage.CatalogEntry
	table *storage.Table
	index *storage.Index
	cols  []storage.Column
}

type hit struct {
	id   storage.RowID
	vals []storage.Value
}

// Open reads the schema catalog. A new database has none until the first table.
func Open(pool *storage.Pool, pager *storage.Pager) (*Engine, error) {
	eng := &Engine{
		pool:    pool,
		pager:   pager,
		tables:  map[string]*relation{},
		catalog: pager.Catalog(),
	}
	if eng.catalog == 0 {
		return eng, nil
	}
	entries, err := storage.LoadCatalog(pool, eng.catalog)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if _, err := eng.openRelation(entry); err != nil {
			return nil, err
		}
	}
	eng.entries = entries
	return eng, nil
}

// Exec parses and runs one statement.
// Outside an explicit transaction the statement commits, or rolls back, before Exec returns.
func (e *Engine) Exec(src string) (*Result, error) {
	stmt, err := parse(src)
	if err != nil {
		return nil, err
	}
	switch stmt.(type) {
	case beginStmt:
		return e.begin()
	case commitStmt:
		return e.commit()
	case rollbackStmt:
		return e.rollback()
	}
	joined := e.pool.Active()
	if !joined {
		if err := e.pool.Begin(); err != nil {
			return nil, err
		}
	}
	res, err := e.execOne(stmt)
	if err != nil {
		e.pool.Rollback()
		if rerr := e.reload(); rerr != nil {
			return nil, rerr
		}
		return nil, err
	}
	if !joined {
		if err := e.pool.Commit(); err != nil {
			e.pool.Rollback()
			_ = e.reload()
			return nil, err
		}
	}
	return res, nil
}

func (e *Engine) begin() (*Result, error) {
	if err := e.pool.BeginExplicit(); err != nil {
		return nil, err
	}
	return &Result{}, nil
}

func (e *Engine) commit() (*Result, error) {
	if !e.pool.Explicit() {
		return nil, storage.ErrNoTxn
	}
	if err := e.pool.Commit(); err != nil {
		return nil, err
	}
	return &Result{}, nil
}

func (e *Engine) rollback() (*Result, error) {
	if !e.pool.Explicit() {
		return nil, storage.ErrNoTxn
	}
	if err := e.pool.Rollback(); err != nil {
		return nil, err
	}
	if err := e.reload(); err != nil {
		return nil, err
	}
	return &Result{}, nil
}

func (e *Engine) execOne(stmt statement) (*Result, error) {
	switch stmt := stmt.(type) {
	case createStmt:
		return e.create(stmt)
	case insertStmt:
		return e.insert(stmt)
	case selectStmt:
		return e.query(stmt)
	case updateStmt:
		return e.update(stmt)
	case deleteStmt:
		return e.remove(stmt)
	default:
		return nil, ErrSyntax
	}
}

// Reload rebuilds table state from the last committed pages.
func (e *Engine) Reload() error { return e.reload() }

func (e *Engine) reload() error {
	e.tables = map[string]*relation{}
	e.entries = nil
	e.catalog = e.pager.Catalog()
	if e.catalog == 0 {
		return nil
	}
	entries, err := storage.LoadCatalog(e.pool, e.catalog)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := e.openRelation(entry); err != nil {
			return err
		}
	}
	e.entries = entries
	return nil
}

func (e *Engine) create(stmt createStmt) (*Result, error) {
	if len(stmt.name) == 0 || len(stmt.name) > 64 || e.tables[stmt.name] != nil {
		return nil, storage.ErrSchema
	}
	if len(stmt.cols) == 0 {
		return nil, storage.ErrSchema
	}
	cols := make([]storage.Column, len(stmt.cols))
	pk := -1
	seen := map[string]bool{}
	for i, col := range stmt.cols {
		if len(col.name) == 0 || len(col.name) > 64 || seen[col.name] {
			return nil, storage.ErrSchema
		}
		seen[col.name] = true
		switch col.kind {
		case "integer":
			cols[i] = storage.Column{Name: col.name, Type: storage.TypeInteger}
		case "text":
			cols[i] = storage.Column{Name: col.name, Type: storage.TypeText}
		default:
			return nil, storage.ErrSchema
		}
		if col.pk {
			if pk >= 0 {
				return nil, storage.ErrSchema
			}
			pk = i
		}
	}
	if pk < 0 {
		return nil, storage.ErrSchema
	}
	table, err := storage.CreateTable(e.pool, cols)
	if err != nil {
		return nil, err
	}
	index, err := storage.CreateIndex(e.pool)
	if err != nil {
		return nil, err
	}
	entry := storage.CatalogEntry{
		Name:   stmt.name,
		Header: table.HeaderID(),
		Index:  index.MetaID(),
		PK:     uint16(pk),
	}
	entries := append(append([]storage.CatalogEntry{}, e.entries...), entry)
	if e.catalog == 0 {
		id, err := storage.CreateCatalog(e.pool, entries)
		if err != nil {
			return nil, err
		}
		if err := e.pool.SetCatalog(id); err != nil {
			return nil, err
		}
		e.catalog = id
	} else if err := storage.SaveCatalog(e.pool, e.catalog, entries); err != nil {
		return nil, err
	}
	e.entries = entries
	e.tables[entry.Name] = &relation{entry: entry, table: table, index: index, cols: table.Columns()}
	return &Result{}, nil
}

func (e *Engine) insert(stmt insertStmt) (*Result, error) {
	rel, err := e.relation(stmt.table)
	if err != nil {
		return nil, err
	}
	vals, err := literals(rel, stmt.vals)
	if err != nil {
		return nil, err
	}
	key, err := storage.IndexKey(rel.cols[rel.entry.PK].Type, vals[rel.entry.PK])
	if err != nil {
		return nil, err
	}
	if _, err := rel.index.Get(key); err == nil {
		return nil, storage.ErrDuplicate
	} else if !errors.Is(err, storage.ErrNotFound) {
		return nil, err
	}
	id, err := rel.table.Insert(vals)
	if err != nil {
		return nil, err
	}
	if err := rel.index.Put(key, id); err != nil {
		_ = rel.table.Delete(id)
		return nil, err
	}
	return &Result{Changed: 1}, nil
}

func (e *Engine) query(stmt selectStmt) (*Result, error) {
	rel, err := e.relation(stmt.table)
	if err != nil {
		return nil, err
	}
	proj, err := projection(rel, stmt)
	if err != nil {
		return nil, err
	}
	hits, err := e.matching(rel, stmt.where)
	if err != nil {
		return nil, err
	}
	if stmt.order != "" {
		col := columnIndex(rel, stmt.order)
		if col < 0 {
			return nil, storage.ErrSchema
		}
		sort.SliceStable(hits, func(i, j int) bool {
			return lessValue(rel.cols[col].Type, hits[i].vals[col], hits[j].vals[col])
		})
	}
	if stmt.limit != nil && *stmt.limit < int64(len(hits)) {
		hits = hits[:int(*stmt.limit)]
	}
	names := make([]string, len(proj))
	rows := make([][]storage.Value, len(hits))
	for i, col := range proj {
		names[i] = rel.cols[col].Name
	}
	for i, h := range hits {
		row := make([]storage.Value, len(proj))
		for j, col := range proj {
			row[j] = cloneValue(h.vals[col])
		}
		rows[i] = row
	}
	return &Result{Columns: names, Rows: rows}, nil
}

func (e *Engine) update(stmt updateStmt) (*Result, error) {
	rel, err := e.relation(stmt.table)
	if err != nil {
		return nil, err
	}
	sets := map[int]literal{}
	for _, set := range stmt.sets {
		col := columnIndex(rel, set.col)
		if col < 0 {
			return nil, storage.ErrSchema
		}
		if _, ok := sets[col]; ok {
			return nil, storage.ErrSchema
		}
		sets[col] = set.value
	}
	hits, err := e.matching(rel, stmt.where)
	if err != nil {
		return nil, err
	}
	type planned struct {
		id     storage.RowID
		next   []storage.Value
		oldKey []byte
		newKey []byte
	}
	plans := make([]planned, len(hits))
	final := map[string]storage.RowID{}
	for i, h := range hits {
		next := cloneValues(h.vals)
		for col, lit := range sets {
			val, err := coerce(rel.cols[col].Type, lit)
			if err != nil {
				return nil, err
			}
			next[col] = val
		}
		oldKey, err := storage.IndexKey(rel.cols[rel.entry.PK].Type, h.vals[rel.entry.PK])
		if err != nil {
			return nil, err
		}
		newKey, err := storage.IndexKey(rel.cols[rel.entry.PK].Type, next[rel.entry.PK])
		if err != nil {
			return nil, err
		}
		if _, ok := final[string(newKey)]; ok {
			return nil, storage.ErrDuplicate
		}
		final[string(newKey)] = h.id
		plans[i] = planned{id: h.id, next: next, oldKey: oldKey, newKey: newKey}
	}
	for key, owner := range final {
		id, err := rel.index.Get([]byte(key))
		if errors.Is(err, storage.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if id != owner {
			kept := false
			for _, h := range hits {
				if h.id == id {
					kept = true
					break
				}
			}
			if !kept {
				return nil, storage.ErrDuplicate
			}
		}
	}
	for _, plan := range plans {
		if err := rel.index.Delete(plan.oldKey); err != nil {
			return nil, err
		}
	}
	for i, plan := range plans {
		id, err := rel.table.Update(plan.id, plan.next)
		if err != nil {
			return nil, err
		}
		plans[i].id = id
	}
	for _, plan := range plans {
		if err := rel.index.Put(plan.newKey, plan.id); err != nil {
			return nil, err
		}
	}
	return &Result{Changed: len(plans)}, nil
}

func (e *Engine) remove(stmt deleteStmt) (*Result, error) {
	rel, err := e.relation(stmt.table)
	if err != nil {
		return nil, err
	}
	hits, err := e.matching(rel, stmt.where)
	if err != nil {
		return nil, err
	}
	for _, h := range hits {
		key, err := storage.IndexKey(rel.cols[rel.entry.PK].Type, h.vals[rel.entry.PK])
		if err != nil {
			return nil, err
		}
		if err := rel.table.Delete(h.id); err != nil {
			return nil, err
		}
		if err := rel.index.Delete(key); err != nil {
			return nil, err
		}
	}
	return &Result{Changed: len(hits)}, nil
}

func (e *Engine) matching(rel *relation, where []predicate) ([]hit, error) {
	for _, pred := range where {
		if columnIndex(rel, pred.col) < 0 {
			return nil, storage.ErrSchema
		}
	}
	if key, use, empty, err := primaryEquality(rel, where); err != nil {
		return nil, err
	} else if empty {
		return nil, nil
	} else if use {
		id, err := rel.index.Get(key)
		if errors.Is(err, storage.ErrNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		vals, err := rel.table.Get(id)
		if err != nil {
			return nil, err
		}
		ok, err := matchAll(rel, vals, where)
		if err != nil || !ok {
			return nil, err
		}
		return []hit{{id: id, vals: vals}}, nil
	}
	var hits []hit
	err := rel.table.Scan(func(id storage.RowID, vals []storage.Value) error {
		ok, err := matchAll(rel, vals, where)
		if err != nil || !ok {
			return err
		}
		hits = append(hits, hit{id: id, vals: cloneValues(vals)})
		return nil
	})
	return hits, err
}

func primaryEquality(rel *relation, where []predicate) (key []byte, use bool, empty bool, err error) {
	pk := rel.cols[rel.entry.PK].Name
	var lit *literal
	for i := range where {
		if where[i].col != pk || where[i].op != "=" {
			continue
		}
		if lit != nil && !sameLiteral(*lit, where[i].value) {
			return nil, false, true, nil
		}
		lit = &where[i].value
	}
	if lit == nil {
		return nil, false, false, nil
	}
	if lit.null {
		return nil, false, true, nil
	}
	val, err := coerce(rel.cols[rel.entry.PK].Type, *lit)
	if err != nil {
		return nil, false, false, err
	}
	key, err = storage.IndexKey(rel.cols[rel.entry.PK].Type, val)
	if err != nil {
		return nil, false, false, err
	}
	return key, true, false, nil
}

func matchAll(rel *relation, vals []storage.Value, where []predicate) (bool, error) {
	for _, pred := range where {
		ok, err := matchOne(rel, vals, pred)
		if err != nil || !ok {
			return false, err
		}
	}
	return true, nil
}

func matchOne(rel *relation, vals []storage.Value, pred predicate) (bool, error) {
	col := columnIndex(rel, pred.col)
	val := vals[col]
	if val.Null || pred.value.null {
		return false, nil
	}
	cmp, err := compare(rel.cols[col].Type, val, pred.value)
	if err != nil {
		return false, err
	}
	switch pred.op {
	case "=":
		return cmp == 0, nil
	case "!=":
		return cmp != 0, nil
	case "<":
		return cmp < 0, nil
	case "<=":
		return cmp <= 0, nil
	case ">":
		return cmp > 0, nil
	case ">=":
		return cmp >= 0, nil
	default:
		return false, ErrSyntax
	}
}

func compare(kind storage.Type, val storage.Value, lit literal) (int, error) {
	switch kind {
	case storage.TypeInteger:
		if !lit.integer {
			return 0, storage.ErrSchema
		}
		switch {
		case val.Int < lit.num:
			return -1, nil
		case val.Int > lit.num:
			return 1, nil
		default:
			return 0, nil
		}
	case storage.TypeText:
		if lit.integer {
			return 0, storage.ErrSchema
		}
		return bytes.Compare(val.Text, lit.text), nil
	default:
		return 0, storage.ErrSchema
	}
}

func lessValue(kind storage.Type, a, b storage.Value) bool {
	if a.Null != b.Null {
		return a.Null
	}
	if a.Null {
		return false
	}
	switch kind {
	case storage.TypeInteger:
		return a.Int < b.Int
	default:
		return bytes.Compare(a.Text, b.Text) < 0
	}
}

func (e *Engine) relation(name string) (*relation, error) {
	rel := e.tables[name]
	if rel == nil {
		return nil, storage.ErrSchema
	}
	return rel, nil
}

func (e *Engine) openRelation(entry storage.CatalogEntry) (*relation, error) {
	table, err := storage.OpenTable(e.pool, entry.Header)
	if err != nil {
		return nil, err
	}
	index, err := storage.OpenIndex(e.pool, entry.Index)
	if err != nil {
		return nil, err
	}
	cols := table.Columns()
	if int(entry.PK) >= len(cols) {
		return nil, storage.ErrCorrupt
	}
	rel := &relation{entry: entry, table: table, index: index, cols: cols}
	e.tables[entry.Name] = rel
	return rel, nil
}

func projection(rel *relation, stmt selectStmt) ([]int, error) {
	if stmt.all {
		out := make([]int, len(rel.cols))
		for i := range rel.cols {
			out[i] = i
		}
		return out, nil
	}
	if len(stmt.cols) == 0 {
		return nil, ErrSyntax
	}
	out := make([]int, len(stmt.cols))
	for i, name := range stmt.cols {
		col := columnIndex(rel, name)
		if col < 0 {
			return nil, storage.ErrSchema
		}
		out[i] = col
	}
	return out, nil
}

func columnIndex(rel *relation, name string) int {
	for i, col := range rel.cols {
		if col.Name == name {
			return i
		}
	}
	return -1
}

func literals(rel *relation, lits []literal) ([]storage.Value, error) {
	if len(lits) != len(rel.cols) {
		return nil, storage.ErrSchema
	}
	vals := make([]storage.Value, len(lits))
	for i, lit := range lits {
		val, err := coerce(rel.cols[i].Type, lit)
		if err != nil {
			return nil, err
		}
		vals[i] = val
	}
	return vals, nil
}

func coerce(kind storage.Type, lit literal) (storage.Value, error) {
	if lit.null {
		return storage.Value{Null: true}, nil
	}
	switch kind {
	case storage.TypeInteger:
		if !lit.integer {
			return storage.Value{}, storage.ErrSchema
		}
		return storage.Value{Int: lit.num}, nil
	case storage.TypeText:
		if lit.integer {
			return storage.Value{}, storage.ErrSchema
		}
		return storage.Value{Text: append([]byte(nil), lit.text...)}, nil
	default:
		return storage.Value{}, storage.ErrSchema
	}
}

func sameLiteral(a, b literal) bool {
	if a.null || b.null {
		return a.null && b.null
	}
	if a.integer || b.integer {
		return a.integer && b.integer && a.num == b.num
	}
	return bytes.Equal(a.text, b.text)
}

func cloneValues(vals []storage.Value) []storage.Value {
	out := make([]storage.Value, len(vals))
	for i, val := range vals {
		out[i] = cloneValue(val)
	}
	return out
}

func cloneValue(val storage.Value) storage.Value {
	if val.Text != nil {
		val.Text = append([]byte(nil), val.Text...)
	}
	return val
}
