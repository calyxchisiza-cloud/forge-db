package storage

import (
	"encoding/binary"
	"errors"
	"sync"
)

// ErrPoolFull means every cached page is pinned, so a miss cannot proceed.
var ErrPoolFull = errors.New("buffer pool has no unpinned frame")

// PoolStats counts how the buffer pool served pages.
type PoolStats struct {
	Hits      int
	Misses    int
	Evictions int
	Flushes   int
	Resident  int
}

type frame struct {
	id    uint32
	data  []byte
	pin   int
	dirty bool
	prev  *frame
	next  *frame
}

// Pool caches pages in a fixed number of frames.
// A pinned page is not evicted. A dirty page is written before its frame is reused.
type Pool struct {
	pager   *Pager
	cap     int
	items   map[uint32]*frame
	head    *frame
	tail    *frame
	stats   PoolStats
	txn     *Txn
	nextTxn uint64
	gate    gate
	busy    recMutex
}

// NewPool caches at most capacity pages from pager.
func NewPool(pager *Pager, capacity int) (*Pool, error) {
	if capacity < 1 {
		return nil, ErrPoolFull
	}
	p := &Pool{
		pager: pager,
		cap:   capacity,
		items: make(map[uint32]*frame),
	}
	p.gate.cond = sync.NewCond(&p.gate.mu)
	p.busy.cond = sync.NewCond(&p.busy.mu)
	return p, nil
}

// PageCount is the number of pages visible to the caller, including a transaction's private pages.
func (p *Pool) PageCount() uint32 {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.limit()
	}
	return p.pager.pageCount
}

// Fetch returns a cached page and pins it. The caller must Unpin it.
// Inside a transaction the returned page is a private copy.
func (p *Pool) Fetch(id uint32) (*Page, error) {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.fetch(id)
	}
	return p.fetchShared(id)
}

func (p *Pool) fetchShared(id uint32) (*Page, error) {
	if f, ok := p.items[id]; ok {
		p.stats.Hits++
		f.pin++
		p.moveFront(f)
		return &Page{data: f.data}, nil
	}
	p.stats.Misses++
	if len(p.items) >= p.cap {
		if err := p.evict(); err != nil {
			return nil, err
		}
	}
	pg, err := p.pager.Read(id)
	if err != nil {
		return nil, err
	}
	f := &frame{id: id, data: pg.data, pin: 1}
	p.items[id] = f
	p.pushFront(f)
	p.stats.Resident = len(p.items)
	return &Page{data: f.data}, nil
}

// Adopt caches a page the pager just created and pins it.
func (p *Pool) Adopt(pg *Page) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.adopt(pg)
	}
	if pg == nil || len(pg.data) != PageSize {
		return ErrCorrupt
	}
	id := pg.ID()
	if f, ok := p.items[id]; ok {
		f.pin++
		f.data = pg.data
		p.moveFront(f)
		return nil
	}
	if len(p.items) >= p.cap {
		if err := p.evict(); err != nil {
			return err
		}
	}
	f := &frame{id: id, data: pg.data, pin: 1}
	p.items[id] = f
	p.pushFront(f)
	p.stats.Resident = len(p.items)
	return nil
}

// Unpin releases one pin. The page stays cached.
func (p *Pool) Unpin(id uint32) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		if _, ok := p.txn.pages[id]; ok {
			if p.txn.pins[id] == 0 {
				return ErrPoolFull
			}
			p.txn.pins[id]--
			return nil
		}
	}
	return p.unpinShared(id)
}

func (p *Pool) unpinShared(id uint32) error {
	f, ok := p.items[id]
	if !ok || f.pin == 0 {
		return ErrPoolFull
	}
	f.pin--
	return nil
}

// MarkDirty remembers that the pinned page differs from the file.
func (p *Pool) MarkDirty(id uint32) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		if _, ok := p.txn.pages[id]; !ok {
			return ErrNotFound
		}
		p.txn.dirty[id] = true
		return nil
	}
	f, ok := p.items[id]
	if !ok {
		return ErrNotFound
	}
	f.dirty = true
	return nil
}

// Store copies a data page into the cache and marks it dirty.
func (p *Pool) Store(pg *Page) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if pg == nil || len(pg.data) != PageSize {
		return ErrCorrupt
	}
	id := pg.ID()
	if id == 0 || id >= p.PageCount() {
		return ErrPageRange
	}
	frame, err := p.Fetch(id)
	if err != nil {
		return err
	}
	defer p.Unpin(id)
	switch pageTypeOf(frame.data) {
	case pageTypeFree:
		return ErrFreedPage
	case pageTypeData:
	default:
		return ErrPageType
	}
	copy(frame.data, pg.data)
	binary.BigEndian.PutUint32(frame.data[offPageID:], id)
	binary.BigEndian.PutUint16(frame.data[offType:], pageTypeData)
	putChecksum(frame.data)
	return p.MarkDirty(id)
}

// Flush writes every dirty page and then syncs the file.
func (p *Pool) Flush() error {
	p.busy.Lock()
	defer p.busy.Unlock()
	for _, f := range p.items {
		if !f.dirty {
			continue
		}
		if err := p.pager.writeRaw(f.data); err != nil {
			return err
		}
		f.dirty = false
		p.stats.Flushes++
	}
	return p.pager.Flush()
}

// Stats returns a copy of the counters.
func (p *Pool) Stats() PoolStats {
	p.busy.Lock()
	defer p.busy.Unlock()
	p.stats.Resident = len(p.items)
	return p.stats
}

// Drop removes a cached page without writing it. A missing page is not an error.
func (p *Pool) Drop(id uint32) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	f, ok := p.items[id]
	if !ok {
		return nil
	}
	if f.pin != 0 {
		return ErrPoolFull
	}
	p.unlink(f)
	delete(p.items, f.id)
	p.stats.Resident = len(p.items)
	return nil
}

// Release drops a cached page and returns it to the free list.
func (p *Pool) Release(id uint32) error {
	p.busy.Lock()
	defer p.busy.Unlock()
	if p.txn != nil {
		return p.txn.free(id)
	}
	if err := p.Drop(id); err != nil {
		return err
	}
	if err := p.Drop(0); err != nil {
		return err
	}
	return p.pager.Free(id)
}

func (p *Pool) install(image []byte) {
	id := binary.BigEndian.Uint32(image[offPageID:])
	data := append([]byte(nil), image...)
	if f, ok := p.items[id]; ok {
		f.data = data
		f.dirty = true
		p.moveFront(f)
		return
	}
	f := &frame{id: id, data: data, dirty: true}
	p.items[id] = f
	p.pushFront(f)
	p.stats.Resident = len(p.items)
}

func (p *Pool) evict() error {
	for f := p.tail; f != nil; f = f.prev {
		if f.pin != 0 {
			continue
		}
		if f.dirty {
			if err := p.pager.writeRaw(f.data); err != nil {
				return err
			}
			f.dirty = false
			p.stats.Flushes++
		}
		p.unlink(f)
		delete(p.items, f.id)
		p.stats.Evictions++
		p.stats.Resident = len(p.items)
		return nil
	}
	return ErrPoolFull
}

func (p *Pool) pushFront(f *frame) {
	f.prev = nil
	f.next = p.head
	if p.head != nil {
		p.head.prev = f
	} else {
		p.tail = f
	}
	p.head = f
}

func (p *Pool) unlink(f *frame) {
	if f.prev != nil {
		f.prev.next = f.next
	} else {
		p.head = f.next
	}
	if f.next != nil {
		f.next.prev = f.prev
	} else {
		p.tail = f.prev
	}
	f.prev = nil
	f.next = nil
}

func (p *Pool) moveFront(f *frame) {
	if p.head == f {
		return
	}
	p.unlink(f)
	p.pushFront(f)
}
