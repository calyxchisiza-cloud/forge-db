package storage

import "encoding/binary"

const (
	slottedNext    = 24
	slottedSlots   = 28
	slottedFree    = 30
	slottedRecords = 32
	slotSize       = 4

	tableCols  = 24
	tableFirst = 26
	tableLast  = 30
	tableCols0 = 34
)

// RowID locates a live row. A delete leaves the slot empty, so later rows keep their identifiers.
type RowID struct {
	Page uint32
	Slot uint16
}

// Table stores rows in slotted pages. The column list is kept in the table header page.
type Table struct {
	pager  *Pager
	header uint32
	cols   []Column
	first  uint32
	last   uint32
}

// CreateTable allocates a header page and stores the column list in it.
func CreateTable(pager *Pager, cols []Column) (*Table, error) {
	if err := validateColumns(cols); err != nil {
		return nil, err
	}
	pg, err := pager.Allocate()
	if err != nil {
		return nil, err
	}
	t := &Table{pager: pager, header: pg.ID(), cols: cloneColumns(cols)}
	if err := t.writeHeader(pg); err != nil {
		return nil, err
	}
	return t, nil
}

// OpenTable loads a table from its header page.
func OpenTable(pager *Pager, header uint32) (*Table, error) {
	pg, err := pager.Read(header)
	if err != nil {
		return nil, err
	}
	if pageTypeOf(pg.data) != pageTypeTable {
		return nil, ErrSchema
	}
	cols, first, last, err := readHeader(pg.data)
	if err != nil {
		return nil, err
	}
	return &Table{pager: pager, header: header, cols: cols, first: first, last: last}, nil
}

// HeaderID is the page that stores this table's columns.
func (t *Table) HeaderID() uint32 { return t.header }

// Columns returns a copy of the column list.
func (t *Table) Columns() []Column { return cloneColumns(t.cols) }

// Insert appends a row and returns its location.
func (t *Table) Insert(vals []Value) (RowID, error) {
	rec, err := encodeRow(t.cols, vals)
	if err != nil {
		return RowID{}, err
	}
	return t.insertRecord(rec, 0)
}

// Get reads the row at id.
func (t *Table) Get(id RowID) ([]Value, error) {
	buf, off, n, err := t.liveSlot(id)
	if err != nil {
		return nil, err
	}
	return decodeRow(t.cols, buf[off:off+n])
}

// Update replaces a row. The same identifier is kept when the new row fits on that page.
func (t *Table) Update(id RowID, vals []Value) (RowID, error) {
	rec, err := encodeRow(t.cols, vals)
	if err != nil {
		return RowID{}, err
	}
	pg, err := t.readSlotted(id.Page)
	if err != nil {
		return RowID{}, err
	}
	count := slotCount(pg.data)
	if int(id.Slot) >= count {
		return RowID{}, ErrNotFound
	}
	_, n := readSlot(pg.data, int(id.Slot))
	if n == 0 {
		return RowID{}, ErrNotFound
	}
	used := int(binary.BigEndian.Uint16(pg.data[slottedFree:])) - slottedRecords
	others := used - int(n)
	capacity := (PageSize - count*slotSize) - slottedRecords
	if others+len(rec) <= capacity {
		putSlot(pg.data, int(id.Slot), 0, 0)
		compact(pg.data)
		place(pg.data, int(id.Slot), rec)
		if err := t.save(pg); err != nil {
			return RowID{}, err
		}
		return id, nil
	}
	moved, err := t.insertRecord(rec, id.Page)
	if err != nil {
		return RowID{}, err
	}
	pg, err = t.readSlotted(id.Page)
	if err != nil {
		return RowID{}, err
	}
	putSlot(pg.data, int(id.Slot), 0, 0)
	compact(pg.data)
	if err := t.save(pg); err != nil {
		return RowID{}, err
	}
	return moved, nil
}

// Delete marks a slot empty and packs the remaining rows on that page.
func (t *Table) Delete(id RowID) error {
	pg, err := t.readSlotted(id.Page)
	if err != nil {
		return err
	}
	if int(id.Slot) >= slotCount(pg.data) {
		return ErrNotFound
	}
	_, n := readSlot(pg.data, int(id.Slot))
	if n == 0 {
		return ErrNotFound
	}
	putSlot(pg.data, int(id.Slot), 0, 0)
	compact(pg.data)
	return t.save(pg)
}

// Scan visits live rows in page order.
func (t *Table) Scan(fn func(RowID, []Value) error) error {
	id := t.first
	seen := map[uint32]bool{}
	for id != 0 {
		if seen[id] || id >= t.pager.pageCount {
			return ErrCorrupt
		}
		seen[id] = true
		pg, err := t.readSlotted(id)
		if err != nil {
			return err
		}
		count := slotCount(pg.data)
		for slot := 0; slot < count; slot++ {
			off, n := readSlot(pg.data, slot)
			if n == 0 {
				continue
			}
			if int(off)+int(n) > PageSize {
				return ErrCorrupt
			}
			vals, err := decodeRow(t.cols, pg.data[int(off):int(off)+int(n)])
			if err != nil {
				return err
			}
			if err := fn(RowID{Page: id, Slot: uint16(slot)}, vals); err != nil {
				return err
			}
		}
		id = binary.BigEndian.Uint32(pg.data[slottedNext:])
	}
	return nil
}

func (t *Table) insertRecord(rec []byte, skip uint32) (RowID, error) {
	id := t.first
	for id != 0 {
		if id == skip {
			pg, err := t.readSlotted(id)
			if err != nil {
				return RowID{}, err
			}
			id = binary.BigEndian.Uint32(pg.data[slottedNext:])
			continue
		}
		pg, err := t.readSlotted(id)
		if err != nil {
			return RowID{}, err
		}
		if slot, ok := placeOnPage(pg.data, rec); ok {
			if err := t.save(pg); err != nil {
				return RowID{}, err
			}
			return RowID{Page: id, Slot: uint16(slot)}, nil
		}
		id = binary.BigEndian.Uint32(pg.data[slottedNext:])
	}
	pg, err := t.pager.Allocate()
	if err != nil {
		return RowID{}, err
	}
	formatSlotted(pg)
	slot, ok := placeOnPage(pg.data, rec)
	if !ok {
		return RowID{}, ErrRowTooLarge
	}
	if err := t.save(pg); err != nil {
		return RowID{}, err
	}
	if t.first == 0 {
		t.first = pg.ID()
		t.last = pg.ID()
	} else {
		prev, err := t.readSlotted(t.last)
		if err != nil {
			return RowID{}, err
		}
		binary.BigEndian.PutUint32(prev.data[slottedNext:], pg.ID())
		if err := t.save(prev); err != nil {
			return RowID{}, err
		}
		t.last = pg.ID()
	}
	if err := t.persistHeader(); err != nil {
		return RowID{}, err
	}
	return RowID{Page: pg.ID(), Slot: uint16(slot)}, nil
}

func (t *Table) liveSlot(id RowID) ([]byte, int, int, error) {
	pg, err := t.readSlotted(id.Page)
	if err != nil {
		return nil, 0, 0, err
	}
	if int(id.Slot) >= slotCount(pg.data) {
		return nil, 0, 0, ErrNotFound
	}
	off, n := readSlot(pg.data, int(id.Slot))
	if n == 0 {
		return nil, 0, 0, ErrNotFound
	}
	return pg.data, int(off), int(n), nil
}

func (t *Table) readSlotted(id uint32) (*Page, error) {
	pg, err := t.pager.Read(id)
	if err != nil {
		return nil, err
	}
	if pageTypeOf(pg.data) != pageTypeSlotted {
		return nil, ErrNotFound
	}
	return pg, nil
}

func (t *Table) save(pg *Page) error {
	putChecksum(pg.data)
	return t.pager.writeRaw(pg.data)
}

func (t *Table) persistHeader() error {
	pg, err := t.pager.Read(t.header)
	if err != nil {
		return err
	}
	return t.writeHeader(pg)
}

func (t *Table) writeHeader(pg *Page) error {
	for i := offPayload; i < PageSize; i++ {
		pg.data[i] = 0
	}
	binary.BigEndian.PutUint16(pg.data[offType:], pageTypeTable)
	binary.BigEndian.PutUint16(pg.data[tableCols:], uint16(len(t.cols)))
	binary.BigEndian.PutUint32(pg.data[tableFirst:], t.first)
	binary.BigEndian.PutUint32(pg.data[tableLast:], t.last)
	pos := tableCols0
	for _, col := range t.cols {
		if pos+2+len(col.Name) > PageSize {
			return ErrSchema
		}
		pg.data[pos] = byte(col.Type)
		pg.data[pos+1] = byte(len(col.Name))
		copy(pg.data[pos+2:], col.Name)
		pos += 2 + len(col.Name)
	}
	putChecksum(pg.data)
	return t.pager.writeRaw(pg.data)
}

func readHeader(buf []byte) ([]Column, uint32, uint32, error) {
	n := int(binary.BigEndian.Uint16(buf[tableCols:]))
	if n == 0 || n > maxColumns {
		return nil, 0, 0, ErrCorrupt
	}
	first := binary.BigEndian.Uint32(buf[tableFirst:])
	last := binary.BigEndian.Uint32(buf[tableLast:])
	pos := tableCols0
	cols := make([]Column, 0, n)
	for i := 0; i < n; i++ {
		if pos+2 > PageSize {
			return nil, 0, 0, ErrCorrupt
		}
		typ := Type(buf[pos])
		nameLen := int(buf[pos+1])
		pos += 2
		if nameLen == 0 || pos+nameLen > PageSize {
			return nil, 0, 0, ErrCorrupt
		}
		cols = append(cols, Column{Name: string(buf[pos : pos+nameLen]), Type: typ})
		pos += nameLen
	}
	if err := validateColumns(cols); err != nil {
		return nil, 0, 0, ErrCorrupt
	}
	return cols, first, last, nil
}

func formatSlotted(pg *Page) {
	for i := offPayload; i < PageSize; i++ {
		pg.data[i] = 0
	}
	binary.BigEndian.PutUint16(pg.data[offType:], pageTypeSlotted)
	binary.BigEndian.PutUint16(pg.data[slottedFree:], slottedRecords)
}

func placeOnPage(buf []byte, rec []byte) (int, bool) {
	count := slotCount(buf)
	for i := 0; i < count; i++ {
		_, n := readSlot(buf, i)
		if n == 0 && fits(buf, len(rec), false) {
			place(buf, i, rec)
			return i, true
		}
	}
	if !fits(buf, len(rec), true) {
		return 0, false
	}
	place(buf, count, rec)
	binary.BigEndian.PutUint16(buf[slottedSlots:], uint16(count+1))
	return count, true
}

func fits(buf []byte, n int, extraSlot bool) bool {
	count := slotCount(buf)
	dir := PageSize - count*slotSize
	if extraSlot {
		dir -= slotSize
	}
	free := int(binary.BigEndian.Uint16(buf[slottedFree:]))
	return free+n <= dir
}

func place(buf []byte, slot int, rec []byte) {
	free := int(binary.BigEndian.Uint16(buf[slottedFree:]))
	copy(buf[free:], rec)
	putSlot(buf, slot, uint16(free), uint16(len(rec)))
	binary.BigEndian.PutUint16(buf[slottedFree:], uint16(free+len(rec)))
}

func compact(buf []byte) {
	count := slotCount(buf)
	type piece struct {
		slot int
		data []byte
	}
	var pieces []piece
	for i := 0; i < count; i++ {
		off, n := readSlot(buf, i)
		if n == 0 {
			continue
		}
		pieces = append(pieces, piece{i, append([]byte(nil), buf[off:int(off)+int(n)]...)})
	}
	dir := PageSize - count*slotSize
	for i := slottedRecords; i < dir; i++ {
		buf[i] = 0
	}
	free := slottedRecords
	for _, pc := range pieces {
		copy(buf[free:], pc.data)
		putSlot(buf, pc.slot, uint16(free), uint16(len(pc.data)))
		free += len(pc.data)
	}
	binary.BigEndian.PutUint16(buf[slottedFree:], uint16(free))
}

func slotCount(buf []byte) int {
	return int(binary.BigEndian.Uint16(buf[slottedSlots:]))
}

func readSlot(buf []byte, i int) (uint16, uint16) {
	pos := PageSize - (i+1)*slotSize
	return binary.BigEndian.Uint16(buf[pos:]), binary.BigEndian.Uint16(buf[pos+2:])
}

func putSlot(buf []byte, i int, off, n uint16) {
	pos := PageSize - (i+1)*slotSize
	binary.BigEndian.PutUint16(buf[pos:], off)
	binary.BigEndian.PutUint16(buf[pos+2:], n)
}

func cloneColumns(cols []Column) []Column {
	out := make([]Column, len(cols))
	copy(out, cols)
	return out
}
