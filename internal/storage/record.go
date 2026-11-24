package storage

import "encoding/binary"

const (
	maxColumns = 32
	maxNameLen = 64
)

// Type is the kind of value stored in a column.
type Type uint8

const (
	// TypeInteger is a signed 64-bit integer.
	TypeInteger Type = 1
	// TypeText is a length-prefixed byte string.
	TypeText Type = 2
)

// Column is one field in a table.
type Column struct {
	Name string
	Type Type
}

// Value is one column value. Null is independent of Int and Text.
type Value struct {
	Null bool
	Int  int64
	Text []byte
}

func encodeRow(cols []Column, vals []Value) ([]byte, error) {
	if len(vals) != len(cols) {
		return nil, ErrSchema
	}
	bitmapLen := (len(cols) + 7) / 8
	body := make([]byte, 0, 32)
	bitmap := make([]byte, bitmapLen)
	for i, col := range cols {
		if vals[i].Null {
			bitmap[i/8] |= 1 << uint(i%8)
			continue
		}
		switch col.Type {
		case TypeInteger:
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(vals[i].Int))
			body = append(body, b[:]...)
		case TypeText:
			text := vals[i].Text
			if len(text) > maxRowBytes() {
				return nil, ErrRowTooLarge
			}
			var n [4]byte
			binary.BigEndian.PutUint32(n[:], uint32(len(text)))
			body = append(body, n[:]...)
			body = append(body, text...)
		default:
			return nil, ErrSchema
		}
	}
	row := append(bitmap, body...)
	if len(row) > maxRowBytes() {
		return nil, ErrRowTooLarge
	}
	return row, nil
}

func decodeRow(cols []Column, row []byte) ([]Value, error) {
	bitmapLen := (len(cols) + 7) / 8
	if len(row) < bitmapLen {
		return nil, ErrCorrupt
	}
	vals := make([]Value, len(cols))
	pos := bitmapLen
	for i, col := range cols {
		if row[i/8]&(1<<uint(i%8)) != 0 {
			vals[i].Null = true
			continue
		}
		switch col.Type {
		case TypeInteger:
			if pos+8 > len(row) {
				return nil, ErrCorrupt
			}
			vals[i].Int = int64(binary.BigEndian.Uint64(row[pos : pos+8]))
			pos += 8
		case TypeText:
			if pos+4 > len(row) {
				return nil, ErrCorrupt
			}
			n := int(binary.BigEndian.Uint32(row[pos : pos+4]))
			pos += 4
			if n < 0 || pos+n > len(row) {
				return nil, ErrCorrupt
			}
			vals[i].Text = append([]byte(nil), row[pos:pos+n]...)
			pos += n
		default:
			return nil, ErrCorrupt
		}
	}
	if pos != len(row) {
		return nil, ErrCorrupt
	}
	return vals, nil
}

func validateColumns(cols []Column) error {
	if len(cols) == 0 || len(cols) > maxColumns {
		return ErrSchema
	}
	seen := make(map[string]bool, len(cols))
	for _, col := range cols {
		if col.Name == "" || len(col.Name) > maxNameLen {
			return ErrSchema
		}
		if col.Type != TypeInteger && col.Type != TypeText {
			return ErrSchema
		}
		if seen[col.Name] {
			return ErrSchema
		}
		seen[col.Name] = true
	}
	return nil
}

func maxRowBytes() int {
	return PageSize - slottedRecords - slotSize
}
