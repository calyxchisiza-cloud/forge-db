package storage

import (
	"encoding/binary"
	"errors"
	"sort"
)

var (
	// ErrNoTxn means commit or rollback was used with no transaction.
	ErrNoTxn = errors.New("no transaction is open")

	// ErrTxnOpen means a transaction is already open.
	ErrTxnOpen = errors.New("a transaction is already open")
)

// Txn is a private set of page copies. Commit appends those pages to the log
// and syncs it before the call returns. The database file is updated later.
type Txn struct {
	pool     *Pool
	id       uint64
	explicit bool
	pages    map[uint32]*Page
	dirty    map[uint32]bool
	pins     map[uint32]int
}

// Begin starts a transaction. Its changes stay private until Commit.
func (p *Pool) Begin() error {
	return p.begin(false)
}

// BeginExplicit starts a transaction that stays open across statements.
func (p *Pool) BeginExplicit() error {
	return p.begin(true)
}

func (p *Pool) begin(explicit bool) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return ErrTxnOpen
	}
	p.nextTxn++
	p.txn = &Txn{
		pool:     p,
		id:       p.nextTxn,
		explicit: explicit,
		pages:    map[uint32]*Page{},
		dirty:    map[uint32]bool{},
		pins:     map[uint32]int{},
	}
	return nil
}

// Active reports whether a transaction is open.
func (p *Pool) Active() bool {
	p.busy.Lock()
	defer p.busy.Unlock()
	return p.txn != nil
}

// Explicit reports whether the open transaction was started for more than one statement.
func (p *Pool) Explicit() bool {
	p.busy.Lock()
	defer p.busy.Unlock()
	return p.txn != nil && p.txn.explicit
}

// Rollback discards private page copies.
func (p *Pool) Rollback() error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn == nil {
		return ErrNoTxn
	}
	p.txn = nil
	return nil
}

// Commit writes a full image of each changed page and a commit record, then syncs the log.
func (p *Pool) Commit() error {
	p.busy.Lock()
	defer p.busy.Unlock()
	t := p.txn
	if t == nil {
		return ErrNoTxn
	}
	if len(t.dirty) == 0 {
		p.txn = nil
		return nil
	}
	if _, err := t.header(); err != nil {
		return err
	}
	t.dirty[0] = true
	ids := make([]uint32, 0, len(t.dirty))
	for id := range t.dirty {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	base := p.pager.nextLSN
	next := base + uint64(len(ids)) + 1
	hdr := t.pages[0]
	binary.BigEndian.PutUint64(hdr.data[offNextLSN:], next)
	pages := make([]walPage, 0, len(ids))
	lsn := base
	for _, id := range ids {
		pg := t.pages[id]
		binary.BigEndian.PutUint64(pg.data[offLSN:], lsn)
		putChecksum(pg.data)
		pages = append(pages, walPage{
			txn:   t.id,
			lsn:   lsn,
			id:    id,
			image: append([]byte(nil), pg.data...),
		})
		lsn++
	}
	commitLSN := lsn
	if err := writeCommit(p.pager.path, t.id, pages, commitLSN); err != nil {
		return err
	}
	p.pager.adoptHeader(hdr.data)
	for _, pg := range pages {
		p.install(pg.image)
	}
	p.txn = nil
	return nil
}

// Checkpoint writes committed pages into the database file and clears the log.
func (p *Pool) Checkpoint() error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return ErrTxnOpen
	}
	if err := p.Flush(); err != nil {
		return err
	}
	return truncateWAL(p.pager.path)
}

// Allocate reserves a page. Inside a transaction the file is unchanged.
func (p *Pool) Allocate() (*Page, error) {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.allocate()
	}
	return p.pager.Allocate()
}

// SetCatalog records the schema page. Inside a transaction the file is unchanged.
func (p *Pool) SetCatalog(id uint32) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.setCatalog(id)
	}
	if err := p.pager.SetCatalog(id); err != nil {
		return err
	}
	return p.Drop(0)
}

func (t *Txn) fetch(id uint32) (*Page, error) {
	if pg, ok := t.pages[id]; ok {
		t.pins[id]++
		return pg, nil
	}
	if id >= t.limit() {
		return nil, ErrPageRange
	}
	shared, err := t.pool.fetchShared(id)
	if err != nil {
		return nil, err
	}
	pg := ClonePage(shared)
	if err := t.pool.unpinShared(id); err != nil {
		return nil, err
	}
	t.pages[id] = pg
	t.pins[id] = 1
	return pg, nil
}

func (t *Txn) allocate() (*Page, error) {
	hdr, err := t.header()
	if err != nil {
		return nil, err
	}
	count := binary.BigEndian.Uint32(hdr.data[offPageCount:])
	freeHead := binary.BigEndian.Uint32(hdr.data[offFreeHead:])
	var pg *Page
	if freeHead != 0 {
		cur, err := t.read(freeHead)
		if err != nil {
			return nil, err
		}
		if pageTypeOf(cur.data) != pageTypeFree {
			return nil, ErrCorrupt
		}
		next := binary.BigEndian.Uint32(cur.data[offPayload:])
		binary.BigEndian.PutUint32(hdr.data[offFreeHead:], next)
		pg = newDataPage(freeHead)
	} else {
		pg = newDataPage(count)
		binary.BigEndian.PutUint32(hdr.data[offPageCount:], count+1)
	}
	putChecksum(hdr.data)
	t.pages[pg.ID()] = pg
	t.dirty[0] = true
	t.dirty[pg.ID()] = true
	return pg, nil
}

func (t *Txn) free(id uint32) error {
	if id == 0 {
		return ErrFreeFileHeader
	}
	hdr, err := t.header()
	if err != nil {
		return err
	}
	if id >= binary.BigEndian.Uint32(hdr.data[offPageCount:]) {
		return ErrPageRange
	}
	cur, err := t.read(id)
	if err != nil {
		return err
	}
	if pageTypeOf(cur.data) == pageTypeFree {
		return ErrDoubleFree
	}
	freeHead := binary.BigEndian.Uint32(hdr.data[offFreeHead:])
	buf := cur.data
	for i := range buf {
		buf[i] = 0
	}
	binary.BigEndian.PutUint32(buf[offPageID:], id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeFree)
	binary.BigEndian.PutUint32(buf[offPayload:], freeHead)
	putChecksum(buf)
	binary.BigEndian.PutUint32(hdr.data[offFreeHead:], id)
	putChecksum(hdr.data)
	t.pages[id] = cur
	t.dirty[id] = true
	t.dirty[0] = true
	return nil
}

func (t *Txn) setCatalog(id uint32) error {
	hdr, err := t.header()
	if err != nil {
		return err
	}
	count := binary.BigEndian.Uint32(hdr.data[offPageCount:])
	if id == 0 || id >= count {
		return ErrPageRange
	}
	binary.BigEndian.PutUint32(hdr.data[offCatalog:], id)
	putChecksum(hdr.data)
	t.dirty[0] = true
	return nil
}

func (t *Txn) adopt(pg *Page) error {
	if pg == nil || len(pg.data) != PageSize {
		return ErrCorrupt
	}
	id := pg.ID()
	t.pages[id] = pg
	t.pins[id]++
	t.dirty[id] = true
	return nil
}

func (t *Txn) header() (*Page, error) {
	if pg, ok := t.pages[0]; ok {
		return pg, nil
	}
	shared, err := t.pool.fetchShared(0)
	if err != nil {
		return nil, err
	}
	pg := ClonePage(shared)
	if err := t.pool.unpinShared(0); err != nil {
		return nil, err
	}
	t.pages[0] = pg
	return pg, nil
}

func (t *Txn) read(id uint32) (*Page, error) {
	if pg, ok := t.pages[id]; ok {
		return pg, nil
	}
	if id >= t.limit() {
		return nil, ErrPageRange
	}
	shared, err := t.pool.fetchShared(id)
	if err != nil {
		return nil, err
	}
	pg := ClonePage(shared)
	if err := t.pool.unpinShared(id); err != nil {
		return nil, err
	}
	t.pages[id] = pg
	return pg, nil
}

func (t *Txn) limit() uint32 {
	if pg, ok := t.pages[0]; ok {
		return binary.BigEndian.Uint32(pg.data[offPageCount:])
	}
	return t.pool.pager.pageCount
}
