package sql

import "strings"

type statement interface{ statement() }

type createStmt struct {
	name string
	cols []columnDef
}

type columnDef struct {
	name string
	kind string
	pk   bool
}

type insertStmt struct {
	table string
	vals  []literal
}

type selectStmt struct {
	table    string
	cols     []string
	all      bool
	where    []predicate
	order    string
	limit    *int64
	hasLimit bool
}

type updateStmt struct {
	table string
	sets  []assignment
	where []predicate
}

type deleteStmt struct {
	table string
	where []predicate
}

type assignment struct {
	col   string
	value literal
}

type predicate struct {
	col   string
	op    string
	value literal
}

type literal struct {
	null    bool
	integer bool
	num     int64
	text    []byte
}

type beginStmt struct{}
type commitStmt struct{}
type rollbackStmt struct{}

func (createStmt) statement()   {}
func (insertStmt) statement()   {}
func (selectStmt) statement()   {}
func (updateStmt) statement()   {}
func (deleteStmt) statement()   {}
func (beginStmt) statement()    {}
func (commitStmt) statement()   {}
func (rollbackStmt) statement() {}

func parse(src string) (statement, error) {
	toks, err := lex(src)
	if err != nil {
		return nil, err
	}
	p := &parser{toks: toks}
	stmt, err := p.statement()
	if err != nil {
		return nil, err
	}
	if p.peek().kind == tokSemi {
		p.next()
	}
	if p.peek().kind != tokEOF {
		return nil, syntax(p.peek().pos)
	}
	return stmt, nil
}

type parser struct {
	toks []token
	i    int
}

func (p *parser) peek() token { return p.toks[p.i] }

func (p *parser) next() token {
	t := p.toks[p.i]
	if t.kind != tokEOF {
		p.i++
	}
	return t
}

func (p *parser) keyword(word string) bool {
	t := p.peek()
	return t.kind == tokIdent && strings.EqualFold(t.lit, word)
}

func (p *parser) takeKeyword(word string) bool {
	if !p.keyword(word) {
		return false
	}
	p.next()
	return true
}

func (p *parser) expectKeyword(word string) error {
	if !p.takeKeyword(word) {
		return syntax(p.peek().pos)
	}
	return nil
}

func (p *parser) ident() (string, error) {
	t := p.peek()
	if t.kind != tokIdent || t.lit == "" {
		return "", syntax(t.pos)
	}
	p.next()
	return t.lit, nil
}

func (p *parser) statement() (statement, error) {
	switch {
	case p.keyword("begin"):
		p.next()
		return beginStmt{}, nil
	case p.keyword("commit"):
		p.next()
		return commitStmt{}, nil
	case p.keyword("rollback"):
		p.next()
		return rollbackStmt{}, nil
	case p.keyword("create"):
		return p.create()
	case p.keyword("insert"):
		return p.insert()
	case p.keyword("select"):
		return p.selectStmt()
	case p.keyword("update"):
		return p.update()
	case p.keyword("delete"):
		return p.delete()
	default:
		return nil, syntax(p.peek().pos)
	}
}

func (p *parser) create() (statement, error) {
	if err := p.expectKeyword("create"); err != nil {
		return nil, err
	}
	if err := p.expectKeyword("table"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if p.next().kind != tokLParen {
		return nil, syntax(p.peek().pos)
	}
	var cols []columnDef
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		var kind string
		switch {
		case p.takeKeyword("integer"):
			kind = "integer"
		case p.takeKeyword("text"):
			kind = "text"
		default:
			return nil, syntax(p.peek().pos)
		}
		pk := false
		if p.takeKeyword("primary") {
			if err := p.expectKeyword("key"); err != nil {
				return nil, err
			}
			pk = true
		}
		cols = append(cols, columnDef{name: col, kind: kind, pk: pk})
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		break
	}
	if p.next().kind != tokRParen {
		return nil, syntax(p.peek().pos)
	}
	return createStmt{name: name, cols: cols}, nil
}

func (p *parser) insert() (statement, error) {
	if err := p.expectKeyword("insert"); err != nil {
		return nil, err
	}
	if err := p.expectKeyword("into"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("values"); err != nil {
		return nil, err
	}
	if p.next().kind != tokLParen {
		return nil, syntax(p.peek().pos)
	}
	var vals []literal
	for {
		lit, err := p.literal()
		if err != nil {
			return nil, err
		}
		vals = append(vals, lit)
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		break
	}
	if p.next().kind != tokRParen {
		return nil, syntax(p.peek().pos)
	}
	return insertStmt{table: name, vals: vals}, nil
}

func (p *parser) selectStmt() (statement, error) {
	if err := p.expectKeyword("select"); err != nil {
		return nil, err
	}
	stmt := selectStmt{}
	if p.peek().kind == tokStar {
		p.next()
		stmt.all = true
	} else {
		for {
			name, err := p.ident()
			if err != nil {
				return nil, err
			}
			stmt.cols = append(stmt.cols, name)
			if p.peek().kind == tokComma {
				p.next()
				continue
			}
			break
		}
	}
	if err := p.expectKeyword("from"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	stmt.table = name
	where, err := p.where()
	if err != nil {
		return nil, err
	}
	stmt.where = where
	if p.takeKeyword("order") {
		if err := p.expectKeyword("by"); err != nil {
			return nil, err
		}
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		stmt.order = col
	}
	if p.takeKeyword("limit") {
		t := p.next()
		if t.kind != tokNumber || t.num < 0 {
			return nil, syntax(t.pos)
		}
		n := t.num
		stmt.limit = &n
		stmt.hasLimit = true
	}
	return stmt, nil
}

func (p *parser) update() (statement, error) {
	if err := p.expectKeyword("update"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	if err := p.expectKeyword("set"); err != nil {
		return nil, err
	}
	var sets []assignment
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		if p.next().kind != tokEq {
			return nil, syntax(p.peek().pos)
		}
		lit, err := p.literal()
		if err != nil {
			return nil, err
		}
		sets = append(sets, assignment{col: col, value: lit})
		if p.peek().kind == tokComma {
			p.next()
			continue
		}
		break
	}
	where, err := p.where()
	if err != nil {
		return nil, err
	}
	return updateStmt{table: name, sets: sets, where: where}, nil
}

func (p *parser) delete() (statement, error) {
	if err := p.expectKeyword("delete"); err != nil {
		return nil, err
	}
	if err := p.expectKeyword("from"); err != nil {
		return nil, err
	}
	name, err := p.ident()
	if err != nil {
		return nil, err
	}
	where, err := p.where()
	if err != nil {
		return nil, err
	}
	return deleteStmt{table: name, where: where}, nil
}

func (p *parser) where() ([]predicate, error) {
	if !p.takeKeyword("where") {
		return nil, nil
	}
	var preds []predicate
	for {
		col, err := p.ident()
		if err != nil {
			return nil, err
		}
		op, err := p.op()
		if err != nil {
			return nil, err
		}
		lit, err := p.literal()
		if err != nil {
			return nil, err
		}
		preds = append(preds, predicate{col: col, op: op, value: lit})
		if !p.takeKeyword("and") {
			break
		}
	}
	return preds, nil
}

func (p *parser) op() (string, error) {
	t := p.next()
	switch t.kind {
	case tokEq, tokNe, tokLt, tokLe, tokGt, tokGe:
		return t.lit, nil
	default:
		return "", syntax(t.pos)
	}
}

func (p *parser) literal() (literal, error) {
	if p.takeKeyword("null") {
		return literal{null: true}, nil
	}
	t := p.next()
	switch t.kind {
	case tokNumber:
		return literal{integer: true, num: t.num}, nil
	case tokString:
		return literal{text: []byte(t.lit)}, nil
	default:
		return literal{}, syntax(t.pos)
	}
}
