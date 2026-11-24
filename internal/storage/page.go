package storage

import (
	"encoding/binary"
	"hash/crc32"
)

const (
	// PageSize is the fixed size of every database page.
	PageSize = 4096

	headerSize    = 24
	formatVersion = 1

	offPageID   = 0
	offType     = 4
	offLSN      = 8
	offChecksum = 16
	offPayload  = 24

	offMagic     = 24
	offVersion   = 28
	offPageSize  = 32
	offPageCount = 36
	offFreeHead  = 40
	offNextLSN   = 44

	pageTypeHeader  uint16 = 1
	pageTypeFree    uint16 = 2
	pageTypeData    uint16 = 3
	pageTypeSlotted uint16 = 4
	pageTypeTable   uint16 = 5
)

const magic = "FGDB"

// Page is one 4 KB database page. Payload is the bytes after the page header.
type Page struct {
	data []byte
}

// ID returns the page identifier stored in the header.
func (pg *Page) ID() uint32 {
	return binary.BigEndian.Uint32(pg.data[offPageID:])
}

// Payload returns the writable bytes after the page header.
// The header is rewritten when the page is saved.
func (pg *Page) Payload() []byte {
	return pg.data[offPayload:]
}

func newDataPage(id uint32) *Page {
	buf := make([]byte, PageSize)
	binary.BigEndian.PutUint32(buf[offPageID:], id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeData)
	putChecksum(buf)
	return &Page{data: buf}
}

// ClonePage returns a detached copy of a page.
func ClonePage(pg *Page) *Page {
	data := make([]byte, PageSize)
	copy(data, pg.data)
	return &Page{data: data}
}

func pageTypeOf(buf []byte) uint16 {
	return binary.BigEndian.Uint16(buf[offType:])
}

func putChecksum(buf []byte) {
	binary.BigEndian.PutUint32(buf[offChecksum:], 0)
	sum := crc32.ChecksumIEEE(buf)
	binary.BigEndian.PutUint32(buf[offChecksum:], sum)
}

func checksumOK(buf []byte) bool {
	want := binary.BigEndian.Uint32(buf[offChecksum:])
	tmp := make([]byte, len(buf))
	copy(tmp, buf)
	binary.BigEndian.PutUint32(tmp[offChecksum:], 0)
	return crc32.ChecksumIEEE(tmp) == want
}
