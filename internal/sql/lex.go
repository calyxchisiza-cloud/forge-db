package sql

import (
	"fmt"
	"math"
	"unicode"
	"unicode/utf8"
)

const (
	tokEOF = iota
	tokIdent
	tokNumber
	tokString
	tokComma
	tokLParen
	tokRParen
	tokStar
	tokEq
	tokNe
	tokLt
	tokLe
	tokGt
	tokGe
	tokSemi
)

type token struct {
	kind int
	lit  string
	num  int64
	pos  int
}

func lex(src string) ([]token, error) {
	out := make([]token, 0, 16)
	i := 0
	for i < len(src) {
		r, w := utf8.DecodeRuneInString(src[i:])
		if r == utf8.RuneError && w == 1 {
			return nil, syntax(i)
		}
		if unicode.IsSpace(r) {
			i += w
			continue
		}
		switch r {
		case ',':
			out = append(out, token{kind: tokComma, pos: i})
			i++
		case '(':
			out = append(out, token{kind: tokLParen, pos: i})
			i++
		case ')':
			out = append(out, token{kind: tokRParen, pos: i})
			i++
		case '*':
			out = append(out, token{kind: tokStar, pos: i})
			i++
		case ';':
			out = append(out, token{kind: tokSemi, pos: i})
			i++
		case '=':
			out = append(out, token{kind: tokEq, lit: "=", pos: i})
			i++
		case '!':
			if i+1 >= len(src) || src[i+1] != '=' {
				return nil, syntax(i)
			}
			out = append(out, token{kind: tokNe, lit: "!=", pos: i})
			i += 2
		case '<':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: tokLe, lit: "<=", pos: i})
				i += 2
			} else {
				out = append(out, token{kind: tokLt, lit: "<", pos: i})
				i++
			}
		case '>':
			if i+1 < len(src) && src[i+1] == '=' {
				out = append(out, token{kind: tokGe, lit: ">=", pos: i})
				i += 2
			} else {
				out = append(out, token{kind: tokGt, lit: ">", pos: i})
				i++
			}
		case '\'':
			lit, next, err := lexString(src, i)
			if err != nil {
				return nil, err
			}
			out = append(out, token{kind: tokString, lit: lit, pos: i})
			i = next
		default:
			if r == '-' || (r >= '0' && r <= '9') {
				num, next, ok := lexNumber(src, i)
				if ok {
					out = append(out, token{kind: tokNumber, num: num, pos: i})
					i = next
					continue
				}
				if r == '-' {
					return nil, syntax(i)
				}
			}
			if r == '_' || unicode.IsLetter(r) {
				j := i + w
				for j < len(src) {
					rr, ww := utf8.DecodeRuneInString(src[j:])
					if rr != '_' && !unicode.IsLetter(rr) && !unicode.IsDigit(rr) {
						break
					}
					j += ww
				}
				out = append(out, token{kind: tokIdent, lit: src[i:j], pos: i})
				i = j
				continue
			}
			return nil, syntax(i)
		}
	}
	out = append(out, token{kind: tokEOF, pos: len(src)})
	return out, nil
}

func lexString(src string, i int) (string, int, error) {
	var b []byte
	for j := i + 1; j < len(src); j++ {
		if src[j] == '\'' {
			if j+1 < len(src) && src[j+1] == '\'' {
				b = append(b, '\'')
				j++
				continue
			}
			return string(b), j + 1, nil
		}
		b = append(b, src[j])
	}
	return "", 0, syntax(i)
}

func lexNumber(src string, i int) (int64, int, bool) {
	j := i
	sign := int64(1)
	if src[j] == '-' {
		if j+1 >= len(src) || src[j+1] < '0' || src[j+1] > '9' {
			return 0, 0, false
		}
		sign = -1
		j++
	}
	if j >= len(src) || src[j] < '0' || src[j] > '9' {
		return 0, 0, false
	}
	var n int64
	for j < len(src) && src[j] >= '0' && src[j] <= '9' {
		d := int64(src[j] - '0')
		if sign > 0 {
			if n > math.MaxInt64/10 || (n == math.MaxInt64/10 && d > math.MaxInt64%10) {
				return 0, 0, false
			}
			n = n*10 + d
		} else if n < math.MinInt64/10 || (n == math.MinInt64/10 && d > -(math.MinInt64%10)) {
			return 0, 0, false
		} else {
			n = n*10 - d
		}
		j++
	}
	if sign > 0 {
		return n, j, true
	}
	return n, j, true
}

func syntax(pos int) error {
	return fmt.Errorf("%w at %d", ErrSyntax, pos)
}
