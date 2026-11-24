package storage

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
)

var (
	// ErrCorrupt means the file, a page checksum, or the free list is not valid.
	ErrCorrupt = errors.New("database file is corrupt")

	// ErrPageIDMismatch means a page's stored identifier does not match its position.
	ErrPageIDMismatch = errors.New("page identifier does not match its position")

	// ErrPageRange means the page identifier is outside the file.
	ErrPageRange = errors.New("page identifier is outside the file")

	// ErrFreeFileHeader means page 0 was passed to Free.
	ErrFreeFileHeader = errors.New("file header page cannot be freed")

	// ErrDoubleFree means Free was called for a page that is already free.
	ErrDoubleFree = errors.New("page is already free")

	// ErrFreedPage means Write was called for a page on the free list.
	ErrFreedPage = errors.New("page is free")

	// ErrClosed means the pager has been closed.
	ErrClosed = errors.New("database is closed")
)

// Pager reads and writes fixed-size pages in one database file.
// It is safe for one caller. Allocation is made durable by Close or Flush.
type Pager struct {
	f         *os.File
	pageCount uint32
	freeHead  uint32
	nextLSN   uint64
	closed    bool
}

// Create makes a new database file. The file must not already exist.
func Create(path string) (*Pager, error) {
	if path == "" {
		return nil, errors.New("database path is empty")
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	p := &Pager{f: f, pageCount: 1, freeHead: 0, nextLSN: 1}
	if err := p.writeHeader(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	return p, nil
}

// Open reads an existing database file and checks the header and free list.
func Open(path string) (*Pager, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if info.Size() == 0 || info.Size()%PageSize != 0 {
		f.Close()
		return nil, ErrCorrupt
	}
	buf, err := readFull(f, 0)
	if err != nil {
		f.Close()
		return nil, err
	}
	if binary.BigEndian.Uint32(buf[offPageID:]) != 0 || pageTypeOf(buf) != pageTypeHeader {
		f.Close()
		return nil, ErrCorrupt
	}
	if string(buf[offMagic:offMagic+4]) != magic {
		f.Close()
		return nil, ErrCorrupt
	}
	if binary.BigEndian.Uint32(buf[offVersion:]) != formatVersion {
		f.Close()
		return nil, ErrCorrupt
	}
	if binary.BigEndian.Uint32(buf[offPageSize:]) != PageSize {
		f.Close()
		return nil, ErrCorrupt
	}
	count := binary.BigEndian.Uint32(buf[offPageCount:])
	if count == 0 || int64(count)*PageSize != info.Size() {
		f.Close()
		return nil, ErrCorrupt
	}
	freeHead := binary.BigEndian.Uint32(buf[offFreeHead:])
	if freeHead >= count {
		f.Close()
		return nil, ErrCorrupt
	}
	p := &Pager{
		f:         f,
		pageCount: count,
		freeHead:  freeHead,
		nextLSN:   binary.BigEndian.Uint64(buf[offNextLSN:]),
	}
	if err := p.checkFreeList(); err != nil {
		f.Close()
		return nil, err
	}
	return p, nil
}

// Allocate reserves a data page. A free page is reused before the file grows.
func (p *Pager) Allocate() (*Page, error) {
	if err := p.usable(); err != nil {
		return nil, err
	}
	if p.freeHead != 0 {
		return p.reuseFree()
	}
	id := p.pageCount
	pg := newDataPage(id)
	if err := p.writeRaw(pg.data); err != nil {
		return nil, err
	}
	p.pageCount++
	if err := p.writeHeader(); err != nil {
		p.pageCount--
		return nil, err
	}
	return pg, nil
}

// Read loads a page and checks its checksum and identifier.
func (p *Pager) Read(id uint32) (*Page, error) {
	if err := p.usable(); err != nil {
		return nil, err
	}
	if id >= p.pageCount {
		return nil, ErrPageRange
	}
	buf, err := p.readPage(id)
	if err != nil {
		return nil, err
	}
	return &Page{data: buf}, nil
}

// Write stores a data page. The page header is set from the page identifier.
func (p *Pager) Write(pg *Page) error {
	if err := p.usable(); err != nil {
		return err
	}
	if pg == nil || len(pg.data) != PageSize {
		return ErrCorrupt
	}
	id := pg.ID()
	if id == 0 || id >= p.pageCount {
		return ErrPageRange
	}
	current, err := p.readPage(id)
	if err != nil {
		return err
	}
	if pageTypeOf(current) == pageTypeFree {
		return ErrFreedPage
	}
	binary.BigEndian.PutUint32(pg.data[offPageID:], id)
	binary.BigEndian.PutUint16(pg.data[offType:], pageTypeData)
	putChecksum(pg.data)
	return p.writeRaw(pg.data)
}

// Free returns a data page to the free list. Page 0 cannot be freed.
func (p *Pager) Free(id uint32) error {
	if err := p.usable(); err != nil {
		return err
	}
	if id == 0 {
		return ErrFreeFileHeader
	}
	if id >= p.pageCount {
		return ErrPageRange
	}
	current, err := p.readPage(id)
	if err != nil {
		return err
	}
	if pageTypeOf(current) == pageTypeFree {
		return ErrDoubleFree
	}
	buf := make([]byte, PageSize)
	binary.BigEndian.PutUint32(buf[offPageID:], id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeFree)
	binary.BigEndian.PutUint32(buf[offPayload:], p.freeHead)
	putChecksum(buf)
	if err := p.writeRaw(buf); err != nil {
		return err
	}
	previous := p.freeHead
	p.freeHead = id
	if err := p.writeHeader(); err != nil {
		p.freeHead = previous
		return err
	}
	return nil
}

// Flush pushes file data to durable storage.
func (p *Pager) Flush() error {
	if err := p.usable(); err != nil {
		return err
	}
	return p.f.Sync()
}

// Close flushes the file and releases it. A second call does nothing.
func (p *Pager) Close() error {
	if p.closed {
		return nil
	}
	p.closed = true
	syncErr := p.f.Sync()
	closeErr := p.f.Close()
	if syncErr != nil {
		return syncErr
	}
	return closeErr
}

// PageCount includes the file header page.
func (p *Pager) PageCount() uint32 {
	return p.pageCount
}

func (p *Pager) usable() error {
	if p.closed {
		return ErrClosed
	}
	return nil
}

func (p *Pager) reuseFree() (*Page, error) {
	id := p.freeHead
	raw, err := p.readPage(id)
	if err != nil {
		return nil, err
	}
	if pageTypeOf(raw) != pageTypeFree {
		return nil, ErrCorrupt
	}
	next := binary.BigEndian.Uint32(raw[offPayload:])
	previous := p.freeHead
	p.freeHead = next
	if err := p.writeHeader(); err != nil {
		p.freeHead = previous
		return nil, err
	}
	pg := newDataPage(id)
	if err := p.writeRaw(pg.data); err != nil {
		p.freeHead = previous
		_ = p.writeHeader()
		return nil, err
	}
	return pg, nil
}

func (p *Pager) checkFreeList() error {
	seen := make(map[uint32]bool)
	id := p.freeHead
	for id != 0 {
		if id >= p.pageCount || seen[id] {
			return ErrCorrupt
		}
		seen[id] = true
		buf, err := p.readPage(id)
		if err != nil {
			return err
		}
		if pageTypeOf(buf) != pageTypeFree {
			return ErrCorrupt
		}
		id = binary.BigEndian.Uint32(buf[offPayload:])
	}
	return nil
}

func (p *Pager) readPage(id uint32) ([]byte, error) {
	buf, err := readFull(p.f, id)
	if err != nil {
		return nil, err
	}
	got := binary.BigEndian.Uint32(buf[offPageID:])
	if got != id {
		return nil, ErrPageIDMismatch
	}
	return buf, nil
}

func readFull(f *os.File, id uint32) ([]byte, error) {
	buf := make([]byte, PageSize)
	n, err := f.ReadAt(buf, int64(id)*PageSize)
	if n != PageSize {
		return nil, ErrCorrupt
	}
	if err != nil && err != io.EOF {
		return nil, err
	}
	if !checksumOK(buf) {
		return nil, ErrCorrupt
	}
	return buf, nil
}

func (p *Pager) writeRaw(buf []byte) error {
	if len(buf) != PageSize {
		return ErrCorrupt
	}
	id := binary.BigEndian.Uint32(buf[offPageID:])
	_, err := p.f.WriteAt(buf, int64(id)*PageSize)
	return err
}

func (p *Pager) writeHeader() error {
	buf := make([]byte, PageSize)
	binary.BigEndian.PutUint32(buf[offPageID:], 0)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeHeader)
	copy(buf[offMagic:], magic)
	binary.BigEndian.PutUint32(buf[offVersion:], formatVersion)
	binary.BigEndian.PutUint32(buf[offPageSize:], PageSize)
	binary.BigEndian.PutUint32(buf[offPageCount:], p.pageCount)
	binary.BigEndian.PutUint32(buf[offFreeHead:], p.freeHead)
	binary.BigEndian.PutUint64(buf[offNextLSN:], p.nextLSN)
	putChecksum(buf)
	return p.writeRaw(buf)
}
