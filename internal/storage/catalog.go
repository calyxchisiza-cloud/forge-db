package storage

import "encoding/binary"

// CatalogEntry names one table and the pages that hold its rows and primary key.
type CatalogEntry struct {
	Name   string
	Header uint32
	Index  uint32
	PK     uint16
}

// CreateCatalog allocates the schema page and stores entries on it.
func CreateCatalog(pool *Pool, entries []CatalogEntry) (uint32, error) {
	pg, err := pool.Allocate()
	if err != nil {
		return 0, err
	}
	if err := pool.Adopt(pg); err != nil {
		return 0, err
	}
	defer pool.Unpin(pg.ID())
	if err := encodeCatalog(pg.data, pg.ID(), entries); err != nil {
		return 0, err
	}
	putChecksum(pg.data)
	if err := pool.MarkDirty(pg.ID()); err != nil {
		return 0, err
	}
	return pg.ID(), nil
}

// SaveCatalog rewrites the schema page.
func SaveCatalog(pool *Pool, id uint32, entries []CatalogEntry) error {
	pg, err := pool.Fetch(id)
	if err != nil {
		return err
	}
	defer pool.Unpin(id)
	if pageTypeOf(pg.data) != pageTypeCatalog {
		return ErrCorrupt
	}
	if err := encodeCatalog(pg.data, id, entries); err != nil {
		return err
	}
	putChecksum(pg.data)
	return pool.MarkDirty(id)
}

// LoadCatalog reads the schema page.
func LoadCatalog(pool *Pool, id uint32) ([]CatalogEntry, error) {
	pg, err := pool.Fetch(id)
	if err != nil {
		return nil, err
	}
	defer pool.Unpin(id)
	if pageTypeOf(pg.data) != pageTypeCatalog {
		return nil, ErrCorrupt
	}
	return decodeCatalog(pg.data)
}

func encodeCatalog(buf []byte, id uint32, entries []CatalogEntry) error {
	body := 26
	for _, e := range entries {
		if len(e.Name) == 0 || len(e.Name) > maxNameLen {
			return ErrSchema
		}
		body += 1 + len(e.Name) + 10
	}
	if body > PageSize {
		return ErrSchema
	}
	for i := offPayload; i < PageSize; i++ {
		buf[i] = 0
	}
	binary.BigEndian.PutUint32(buf[offPageID:], id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeCatalog)
	binary.BigEndian.PutUint16(buf[offPayload:], uint16(len(entries)))
	off := 26
	for _, e := range entries {
		buf[off] = byte(len(e.Name))
		off++
		copy(buf[off:], e.Name)
		off += len(e.Name)
		binary.BigEndian.PutUint32(buf[off:], e.Header)
		off += 4
		binary.BigEndian.PutUint32(buf[off:], e.Index)
		off += 4
		binary.BigEndian.PutUint16(buf[off:], e.PK)
		off += 2
	}
	return nil
}

func decodeCatalog(buf []byte) ([]CatalogEntry, error) {
	count := int(binary.BigEndian.Uint16(buf[offPayload:]))
	off := 26
	out := make([]CatalogEntry, 0, count)
	seen := map[string]bool{}
	for i := 0; i < count; i++ {
		if off >= PageSize {
			return nil, ErrCorrupt
		}
		n := int(buf[off])
		off++
		if n == 0 || n > maxNameLen || off+n+10 > PageSize {
			return nil, ErrCorrupt
		}
		name := string(buf[off : off+n])
		off += n
		if seen[name] {
			return nil, ErrCorrupt
		}
		seen[name] = true
		e := CatalogEntry{
			Name:   name,
			Header: binary.BigEndian.Uint32(buf[off:]),
			Index:  binary.BigEndian.Uint32(buf[off+4:]),
			PK:     binary.BigEndian.Uint16(buf[off+8:]),
		}
		off += 10
		out = append(out, e)
	}
	return out, nil
}
