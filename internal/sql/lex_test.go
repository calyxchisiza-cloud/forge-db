package sql

import "testing"

func FuzzLexer(f *testing.F) {
	f.Add("SELECT * FROM t WHERE a >= -1 AND b != 'x''y'")
	f.Add("INSERT INTO account VALUES (1, NULL, 'Ada')")
	f.Add("UPDATE account SET balance = 40 WHERE id = 1")
	f.Fuzz(func(t *testing.T, src string) {
		toks, err := lex(src)
		if err != nil {
			return
		}
		if len(toks) == 0 || toks[len(toks)-1].kind != tokEOF {
			t.Fatal("lexer dropped the end of the statement")
		}
		for i, tok := range toks[:len(toks)-1] {
			if tok.pos < 0 || tok.pos > len(src) {
				t.Fatalf("token %d position %d", i, tok.pos)
			}
		}
	})
}
