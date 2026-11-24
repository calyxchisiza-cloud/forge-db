package storage

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

const (
	nodeKind     = 24
	nodeCount    = 26
	nodeRight    = 28
	nodeParent   = 32
	nodeLeft     = 36
	nodeBody     = 40
	indexRootOff = 24

	nodeLeaf     uint16 = 1
	nodeInternal uint16 = 2

	// A leaf entry is 8 bytes plus the key. Two maximum keys fill the body.
	maxKeyBytes  = (PageSize-nodeBody)/2 - 8
	minIndexFill = (PageSize - nodeBody) / 2
)

// IndexKey encodes a primary-key value. Integer order is byte order of the two's-complement form.
func IndexKey(col Type, v Value) ([]byte, error) {
	if v.Null {
		return nil, ErrSchema
	}
	switch col {
	case TypeInteger:
		buf := make([]byte, 8)
		binary.BigEndian.PutUint64(buf, uint64(v.Int))
		return buf, nil
	case TypeText:
		if len(v.Text) > maxKeyBytes {
			return nil, ErrKeyTooLarge
		}
		return append([]byte(nil), v.Text...), nil
	default:
		return nil, ErrSchema
	}
}

// Index maps a byte key to the page and slot of a row.
// The catalog page keeps the current root, so a root split does not change MetaID.
type Index struct {
	pool *Pool
	meta uint32
	root uint32
}

type ixEntry struct {
	key  []byte
	page uint32
	slot uint16
}

type ixNode struct {
	id      uint32
	leaf    bool
	parent  uint32
	right   uint32
	left    uint32
	entries []ixEntry
}

// CreateIndex stores an empty B+Tree and returns its catalog page.
func CreateIndex(pool *Pool) (*Index, error) {
	meta, err := pool.Allocate()
	if err != nil {
		return nil, err
	}
	if err := pool.Adopt(meta); err != nil {
		return nil, err
	}
	rootPage, err := pool.Allocate()
	if err != nil {
		pool.Unpin(meta.ID())
		return nil, err
	}
	if err := pool.Adopt(rootPage); err != nil {
		pool.Unpin(meta.ID())
		return nil, err
	}
	ix := &Index{pool: pool, meta: meta.ID(), root: rootPage.ID()}
	root := &ixNode{id: rootPage.ID(), leaf: true}
	if err := encodeNode(rootPage.data, root); err != nil {
		return nil, err
	}
	putChecksum(rootPage.data)
	if err := pool.MarkDirty(root.id); err != nil {
		return nil, err
	}
	if err := writeCatalog(meta.data, ix.meta, ix.root); err != nil {
		return nil, err
	}
	putChecksum(meta.data)
	if err := pool.MarkDirty(ix.meta); err != nil {
		return nil, err
	}
	if err := pool.Unpin(root.id); err != nil {
		return nil, err
	}
	if err := pool.Unpin(ix.meta); err != nil {
		return nil, err
	}
	return ix, nil
}

// OpenIndex loads an index from the catalog page written by CreateIndex.
func OpenIndex(pool *Pool, meta uint32) (*Index, error) {
	pg, err := pool.Fetch(meta)
	if err != nil {
		return nil, err
	}
	defer pool.Unpin(meta)
	if pageTypeOf(pg.data) != pageTypeIndex {
		return nil, ErrSchema
	}
	root := binary.BigEndian.Uint32(pg.data[indexRootOff:])
	if root == 0 || root >= pool.PageCount() {
		return nil, ErrCorrupt
	}
	return &Index{pool: pool, meta: meta, root: root}, nil
}

// MetaID is the catalog page. It stays the same when the root splits.
func (ix *Index) MetaID() uint32 { return ix.meta }

// Put inserts a key. The key is copied.
func (ix *Index) Put(key []byte, row RowID) error {
	if len(key) > maxKeyBytes {
		return ErrKeyTooLarge
	}
	leaf, err := ix.findLeaf(key)
	if err != nil {
		return err
	}
	i, found := searchEntries(leaf.entries, key)
	if found {
		return ErrDuplicate
	}
	leaf.entries = insertEntry(leaf.entries, i, ixEntry{
		key:  append([]byte(nil), key...),
		page: row.Page,
		slot: row.Slot,
	})
	if leaf.fits() {
		if err := ix.writeNode(leaf); err != nil {
			return err
		}
		return ix.fixMin(leaf)
	}
	return ix.splitLeaf(leaf)
}

// Get returns the row stored for key.
func (ix *Index) Get(key []byte) (RowID, error) {
	leaf, err := ix.findLeaf(key)
	if err != nil {
		return RowID{}, err
	}
	i, found := searchEntries(leaf.entries, key)
	if !found {
		return RowID{}, ErrNotFound
	}
	e := leaf.entries[i]
	return RowID{Page: e.page, Slot: e.slot}, nil
}

// Delete removes a key. A short page borrows from a sibling or merges with one.
func (ix *Index) Delete(key []byte) error {
	leaf, err := ix.findLeaf(key)
	if err != nil {
		return err
	}
	i, found := searchEntries(leaf.entries, key)
	if !found {
		return ErrNotFound
	}
	leaf.entries = append(leaf.entries[:i], leaf.entries[i+1:]...)
	if err := ix.writeNode(leaf); err != nil {
		return err
	}
	if leaf.parent == 0 || leaf.encoded() >= minIndexFill {
		return ix.fixMin(leaf)
	}
	return ix.rebalance(leaf)
}

// Scan visits keys from smallest to largest.
func (ix *Index) Scan(fn func([]byte, RowID) error) error {
	id, err := ix.leftmostLeaf()
	if err != nil {
		return err
	}
	seen := map[uint32]bool{}
	for id != 0 {
		if seen[id] {
			return ErrCorrupt
		}
		seen[id] = true
		n, err := ix.readNode(id)
		if err != nil {
			return err
		}
		if !n.leaf {
			return ErrCorrupt
		}
		for _, e := range n.entries {
			if err := fn(e.key, RowID{Page: e.page, Slot: e.slot}); err != nil {
				return err
			}
		}
		id = n.right
	}
	return nil
}

// Check walks the tree and rejects a broken parent, separator, order, or sibling link.
func (ix *Index) Check() error {
	pg, err := ix.pool.Fetch(ix.meta)
	if err != nil {
		return err
	}
	stored := binary.BigEndian.Uint32(pg.data[indexRootOff:])
	kind := pageTypeOf(pg.data)
	if err := ix.pool.Unpin(ix.meta); err != nil {
		return err
	}
	if kind != pageTypeIndex || stored != ix.root {
		return fmt.Errorf("%w: index catalog", ErrCorrupt)
	}
	seen := map[uint32]bool{}
	var leafDepth int
	var haveDepth bool
	var leaves []uint32
	if err := ix.checkNode(ix.root, 0, 1, &leafDepth, &haveDepth, seen, &leaves); err != nil {
		return err
	}
	id, err := ix.leftmostLeaf()
	if err != nil {
		return err
	}
	for _, want := range leaves {
		if id != want {
			return fmt.Errorf("%w: leaf sibling", ErrCorrupt)
		}
		n, err := ix.readNode(id)
		if err != nil {
			return err
		}
		id = n.right
	}
	if id != 0 {
		return fmt.Errorf("%w: leaf sibling tail", ErrCorrupt)
	}
	return nil
}

func (ix *Index) checkNode(id, parent uint32, depth int, leafDepth *int, haveDepth *bool, seen map[uint32]bool, leaves *[]uint32) error {
	if id == 0 || seen[id] {
		return fmt.Errorf("%w: index cycle", ErrCorrupt)
	}
	seen[id] = true
	n, err := ix.readNode(id)
	if err != nil {
		return err
	}
	if n.parent != parent {
		return fmt.Errorf("%w: index parent", ErrCorrupt)
	}
	for i := 1; i < len(n.entries); i++ {
		if bytes.Compare(n.entries[i-1].key, n.entries[i].key) >= 0 {
			return fmt.Errorf("%w: index order", ErrCorrupt)
		}
	}
	if n.leaf {
		if !*haveDepth {
			*leafDepth = depth
			*haveDepth = true
		} else if depth != *leafDepth {
			return fmt.Errorf("%w: index height", ErrCorrupt)
		}
		*leaves = append(*leaves, id)
		return nil
	}
	var prevMax []byte
	havePrev := false
	child := n.left
	for i := 0; i <= len(n.entries); i++ {
		if err := ix.checkNode(child, id, depth+1, leafDepth, haveDepth, seen, leaves); err != nil {
			return err
		}
		min, max, err := ix.span(child)
		if err != nil {
			return err
		}
		if havePrev && bytes.Compare(prevMax, min) >= 0 {
			return fmt.Errorf("%w: index separator", ErrCorrupt)
		}
		if i > 0 && !bytes.Equal(n.entries[i-1].key, min) {
			return fmt.Errorf("%w: index separator", ErrCorrupt)
		}
		prevMax = max
		havePrev = true
		if i < len(n.entries) {
			child = n.entries[i].page
		}
	}
	return nil
}

func (ix *Index) span(id uint32) (min, max []byte, err error) {
	n, err := ix.readNode(id)
	if err != nil {
		return nil, nil, err
	}
	if n.leaf {
		if len(n.entries) == 0 {
			return nil, nil, fmt.Errorf("%w: empty leaf", ErrCorrupt)
		}
		return n.entries[0].key, n.entries[len(n.entries)-1].key, nil
	}
	min, _, err = ix.span(n.left)
	if err != nil {
		return nil, nil, err
	}
	last := n.left
	if len(n.entries) > 0 {
		last = n.entries[len(n.entries)-1].page
	}
	_, max, err = ix.span(last)
	return min, max, err
}

func (ix *Index) findLeaf(key []byte) (*ixNode, error) {
	id := ix.root
	for {
		n, err := ix.readNode(id)
		if err != nil {
			return nil, err
		}
		if n.leaf {
			return n, nil
		}
		id = n.child(key)
	}
}

func (ix *Index) leftmostLeaf() (uint32, error) {
	id := ix.root
	for {
		n, err := ix.readNode(id)
		if err != nil {
			return 0, err
		}
		if n.leaf {
			return id, nil
		}
		id = n.left
	}
}

func (ix *Index) splitLeaf(n *ixNode) error {
	if len(n.entries) < 2 {
		return ErrKeyTooLarge
	}
	mid := len(n.entries) / 2
	rightEntries := copyEntries(n.entries[mid:])
	n.entries = copyEntries(n.entries[:mid])
	right, err := ix.newNode(true)
	if err != nil {
		return err
	}
	right.entries = rightEntries
	right.right = n.right
	right.parent = n.parent
	n.right = right.id
	sep := append([]byte(nil), right.entries[0].key...)
	if err := ix.writeNode(n); err != nil {
		return err
	}
	if err := ix.writeNode(right); err != nil {
		return err
	}
	return ix.insertParent(n, sep, right.id)
}

func (ix *Index) splitInternal(n *ixNode) error {
	if len(n.entries) < 3 {
		return ErrCorrupt
	}
	mid := len(n.entries) / 2
	promoted := append([]byte(nil), n.entries[mid].key...)
	rightLeft := n.entries[mid].page
	rightEntries := copyEntries(n.entries[mid+1:])
	n.entries = copyEntries(n.entries[:mid])
	right, err := ix.newNode(false)
	if err != nil {
		return err
	}
	right.left = rightLeft
	right.entries = rightEntries
	right.parent = n.parent
	if err := ix.writeNode(n); err != nil {
		return err
	}
	if err := ix.writeNode(right); err != nil {
		return err
	}
	if err := ix.retarget(right); err != nil {
		return err
	}
	return ix.insertParent(n, promoted, right.id)
}

func (ix *Index) insertParent(left *ixNode, sep []byte, rightID uint32) error {
	if left.parent == 0 {
		root, err := ix.newNode(false)
		if err != nil {
			return err
		}
		root.left = left.id
		root.entries = []ixEntry{{key: sep, page: rightID}}
		left.parent = root.id
		right, err := ix.readNode(rightID)
		if err != nil {
			return err
		}
		right.parent = root.id
		if err := ix.writeNode(left); err != nil {
			return err
		}
		if err := ix.writeNode(right); err != nil {
			return err
		}
		if err := ix.writeNode(root); err != nil {
			return err
		}
		return ix.setRoot(root.id)
	}
	parent, err := ix.readNode(left.parent)
	if err != nil {
		return err
	}
	pos, err := childPos(parent, left.id)
	if err != nil {
		return err
	}
	parent.entries = insertEntry(parent.entries, pos, ixEntry{key: sep, page: rightID})
	if parent.fits() {
		return ix.writeNode(parent)
	}
	return ix.splitInternal(parent)
}

func (ix *Index) rebalance(n *ixNode) error {
	if n.parent == 0 {
		return ix.collapseRoot(n)
	}
	parent, err := ix.readNode(n.parent)
	if err != nil {
		return err
	}
	pos, err := childPos(parent, n.id)
	if err != nil {
		return err
	}
	if pos > 0 {
		left, err := ix.readNode(parent.childAt(pos - 1))
		if err != nil {
			return err
		}
		if canGive(left, n, false, parent, pos-1) {
			return ix.borrow(parent, left, n, pos-1, false)
		}
	}
	if pos+1 < parent.childCount() {
		right, err := ix.readNode(parent.childAt(pos + 1))
		if err != nil {
			return err
		}
		if canGive(right, n, true, parent, pos) {
			return ix.borrow(parent, n, right, pos, true)
		}
	}
	if pos+1 < parent.childCount() {
		right, err := ix.readNode(parent.childAt(pos + 1))
		if err != nil {
			return err
		}
		if canMerge(n, right, parent.entries[pos].key) {
			return ix.merge(parent, n, right, pos)
		}
	}
	if pos > 0 {
		left, err := ix.readNode(parent.childAt(pos - 1))
		if err != nil {
			return err
		}
		if canMerge(left, n, parent.entries[pos-1].key) {
			return ix.merge(parent, left, n, pos-1)
		}
	}
	return ix.fixMin(n)
}

func (ix *Index) borrow(parent, left, right *ixNode, sep int, fromRight bool) error {
	if left.leaf {
		if fromRight {
			moved := right.entries[0]
			right.entries = copyEntries(right.entries[1:])
			left.entries = append(copyEntries(left.entries), moved)
		} else {
			moved := left.entries[len(left.entries)-1]
			left.entries = copyEntries(left.entries[:len(left.entries)-1])
			right.entries = insertEntry(copyEntries(right.entries), 0, moved)
		}
		if err := ix.writeNode(left); err != nil {
			return err
		}
		if err := ix.writeNode(right); err != nil {
			return err
		}
		if err := ix.fixMin(right); err != nil {
			return err
		}
		return ix.fixMin(left)
	}
	sepKey := append([]byte(nil), parent.entries[sep].key...)
	var moved uint32
	if fromRight {
		moved = right.left
		left.entries = append(copyEntries(left.entries), ixEntry{key: sepKey, page: moved})
		right.left = right.entries[0].page
		right.entries = copyEntries(right.entries[1:])
	} else {
		last := left.entries[len(left.entries)-1]
		moved = last.page
		left.entries = copyEntries(left.entries[:len(left.entries)-1])
		right.entries = insertEntry(copyEntries(right.entries), 0, ixEntry{key: sepKey, page: right.left})
		right.left = moved
	}
	if err := ix.writeNode(left); err != nil {
		return err
	}
	if err := ix.writeNode(right); err != nil {
		return err
	}
	child, err := ix.readNode(moved)
	if err != nil {
		return err
	}
	child.parent = parentOfMoved(fromRight, left, right)
	if err := ix.writeNode(child); err != nil {
		return err
	}
	if err := ix.fixMin(right); err != nil {
		return err
	}
	return ix.fixMin(left)
}

func parentOfMoved(fromRight bool, left, right *ixNode) uint32 {
	if fromRight {
		return left.id
	}
	return right.id
}

func (ix *Index) merge(parent, left, right *ixNode, sep int) error {
	if !left.leaf {
		left.entries = append(copyEntries(left.entries), ixEntry{
			key:  append([]byte(nil), parent.entries[sep].key...),
			page: right.left,
		})
		left.entries = append(left.entries, copyEntries(right.entries)...)
	} else {
		left.entries = append(copyEntries(left.entries), copyEntries(right.entries)...)
		left.right = right.right
	}
	parent.entries = append(copyEntries(parent.entries[:sep]), copyEntries(parent.entries[sep+1:])...)
	if !left.fits() || !parent.fits() {
		return ErrCorrupt
	}
	if err := ix.writeNode(left); err != nil {
		return err
	}
	if err := ix.writeNode(parent); err != nil {
		return err
	}
	if !left.leaf {
		if err := ix.retarget(left); err != nil {
			return err
		}
	}
	if err := ix.pool.Release(right.id); err != nil {
		return err
	}
	if err := ix.fixMin(left); err != nil {
		return err
	}
	fresh, err := ix.readNode(parent.id)
	if err != nil {
		return err
	}
	return ix.rebalance(fresh)
}

func (ix *Index) collapseRoot(n *ixNode) error {
	if n.leaf || len(n.entries) != 0 {
		return nil
	}
	child, err := ix.readNode(n.left)
	if err != nil {
		return err
	}
	child.parent = 0
	if err := ix.writeNode(child); err != nil {
		return err
	}
	if err := ix.setRoot(child.id); err != nil {
		return err
	}
	if err := ix.pool.Release(n.id); err != nil {
		return err
	}
	return ix.collapseRoot(child)
}

func (ix *Index) fixMin(n *ixNode) error {
	min, ok, err := ix.firstKey(n)
	if err != nil || !ok {
		return err
	}
	node := n
	for node.parent != 0 {
		parent, err := ix.readNode(node.parent)
		if err != nil {
			return err
		}
		pos, err := childPos(parent, node.id)
		if err != nil {
			return err
		}
		if pos != 0 {
			if bytes.Equal(parent.entries[pos-1].key, min) {
				return nil
			}
			parent.entries[pos-1].key = append([]byte(nil), min...)
			if parent.fits() {
				return ix.writeNode(parent)
			}
			return ix.splitInternal(parent)
		}
		node = parent
	}
	return nil
}

func (ix *Index) firstKey(n *ixNode) ([]byte, bool, error) {
	cur := n
	for !cur.leaf {
		next, err := ix.readNode(cur.left)
		if err != nil {
			return nil, false, err
		}
		cur = next
	}
	if len(cur.entries) == 0 {
		return nil, false, nil
	}
	return cur.entries[0].key, true, nil
}

func (ix *Index) retarget(n *ixNode) error {
	if n.leaf {
		return nil
	}
	ids := []uint32{n.left}
	for _, e := range n.entries {
		ids = append(ids, e.page)
	}
	for _, id := range ids {
		child, err := ix.readNode(id)
		if err != nil {
			return err
		}
		child.parent = n.id
		if err := ix.writeNode(child); err != nil {
			return err
		}
	}
	return nil
}

func (ix *Index) newNode(leaf bool) (*ixNode, error) {
	pg, err := ix.pool.Allocate()
	if err != nil {
		return nil, err
	}
	if err := ix.pool.Adopt(pg); err != nil {
		return nil, err
	}
	n := &ixNode{id: pg.ID(), leaf: leaf}
	if err := encodeNode(pg.data, n); err != nil {
		return nil, err
	}
	putChecksum(pg.data)
	if err := ix.pool.MarkDirty(n.id); err != nil {
		return nil, err
	}
	if err := ix.pool.Unpin(n.id); err != nil {
		return nil, err
	}
	return n, nil
}

func (ix *Index) writeNode(n *ixNode) error {
	pg, err := ix.pool.Fetch(n.id)
	if err != nil {
		return err
	}
	defer ix.pool.Unpin(n.id)
	if err := encodeNode(pg.data, n); err != nil {
		return err
	}
	putChecksum(pg.data)
	return ix.pool.MarkDirty(n.id)
}

func (ix *Index) readNode(id uint32) (*ixNode, error) {
	pg, err := ix.pool.Fetch(id)
	if err != nil {
		return nil, err
	}
	defer ix.pool.Unpin(id)
	if pageTypeOf(pg.data) != pageTypeNode {
		return nil, ErrCorrupt
	}
	return decodeNode(pg.data)
}

func (ix *Index) setRoot(id uint32) error {
	ix.root = id
	pg, err := ix.pool.Fetch(ix.meta)
	if err != nil {
		return err
	}
	defer ix.pool.Unpin(ix.meta)
	if err := writeCatalog(pg.data, ix.meta, id); err != nil {
		return err
	}
	putChecksum(pg.data)
	return ix.pool.MarkDirty(ix.meta)
}

func writeCatalog(buf []byte, id, root uint32) error {
	for i := offPayload; i < PageSize; i++ {
		buf[i] = 0
	}
	binary.BigEndian.PutUint32(buf[offPageID:], id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeIndex)
	binary.BigEndian.PutUint32(buf[indexRootOff:], root)
	return nil
}

func encodeNode(buf []byte, n *ixNode) error {
	if !n.fits() {
		return ErrCorrupt
	}
	for i := offPayload; i < PageSize; i++ {
		buf[i] = 0
	}
	binary.BigEndian.PutUint32(buf[offPageID:], n.id)
	binary.BigEndian.PutUint16(buf[offType:], pageTypeNode)
	kind := nodeInternal
	if n.leaf {
		kind = nodeLeaf
	}
	binary.BigEndian.PutUint16(buf[nodeKind:], kind)
	binary.BigEndian.PutUint16(buf[nodeCount:], uint16(len(n.entries)))
	binary.BigEndian.PutUint32(buf[nodeRight:], n.right)
	binary.BigEndian.PutUint32(buf[nodeParent:], n.parent)
	binary.BigEndian.PutUint32(buf[nodeLeft:], n.left)
	off := nodeBody
	for _, e := range n.entries {
		need := entrySize(n.leaf, e.key)
		if off+need > PageSize {
			return ErrCorrupt
		}
		binary.BigEndian.PutUint16(buf[off:], uint16(len(e.key)))
		off += 2
		copy(buf[off:], e.key)
		off += len(e.key)
		binary.BigEndian.PutUint32(buf[off:], e.page)
		off += 4
		if n.leaf {
			binary.BigEndian.PutUint16(buf[off:], e.slot)
			off += 2
		}
	}
	return nil
}

func decodeNode(buf []byte) (*ixNode, error) {
	n := &ixNode{
		id:     binary.BigEndian.Uint32(buf[offPageID:]),
		parent: binary.BigEndian.Uint32(buf[nodeParent:]),
		right:  binary.BigEndian.Uint32(buf[nodeRight:]),
		left:   binary.BigEndian.Uint32(buf[nodeLeft:]),
	}
	switch binary.BigEndian.Uint16(buf[nodeKind:]) {
	case nodeLeaf:
		n.leaf = true
	case nodeInternal:
	default:
		return nil, ErrCorrupt
	}
	count := int(binary.BigEndian.Uint16(buf[nodeCount:]))
	off := nodeBody
	n.entries = make([]ixEntry, 0, count)
	for i := 0; i < count; i++ {
		if off+2 > PageSize {
			return nil, ErrCorrupt
		}
		klen := int(binary.BigEndian.Uint16(buf[off:]))
		off += 2
		if klen > maxKeyBytes || off+klen+4 > PageSize || (n.leaf && off+klen+6 > PageSize) {
			return nil, ErrCorrupt
		}
		key := append([]byte(nil), buf[off:off+klen]...)
		off += klen
		e := ixEntry{key: key, page: binary.BigEndian.Uint32(buf[off:])}
		off += 4
		if n.leaf {
			e.slot = binary.BigEndian.Uint16(buf[off:])
			off += 2
		}
		n.entries = append(n.entries, e)
	}
	return n, nil
}

func (n *ixNode) encoded() int {
	total := 0
	for _, e := range n.entries {
		total += entrySize(n.leaf, e.key)
	}
	return total
}

func (n *ixNode) fits() bool {
	return n.encoded() <= PageSize-nodeBody
}

func (n *ixNode) child(key []byte) uint32 {
	dest := n.left
	for _, e := range n.entries {
		if bytes.Compare(key, e.key) < 0 {
			return dest
		}
		dest = e.page
	}
	return dest
}

func (n *ixNode) childAt(i int) uint32 {
	if i == 0 {
		return n.left
	}
	return n.entries[i-1].page
}

func (n *ixNode) childCount() int {
	if n.leaf {
		return 0
	}
	return len(n.entries) + 1
}

func childPos(n *ixNode, id uint32) (int, error) {
	if n.left == id {
		return 0, nil
	}
	for i, e := range n.entries {
		if e.page == id {
			return i + 1, nil
		}
	}
	return 0, ErrCorrupt
}

func entrySize(leaf bool, key []byte) int {
	if leaf {
		return 8 + len(key)
	}
	return 6 + len(key)
}

func canGive(donor, receiver *ixNode, fromRight bool, parent *ixNode, sep int) bool {
	if len(donor.entries) == 0 {
		return false
	}
	var moving []byte
	if fromRight {
		moving = donor.entries[0].key
	} else {
		moving = donor.entries[len(donor.entries)-1].key
	}
	lose := entrySize(donor.leaf, moving)
	gain := lose
	if !donor.leaf {
		gain = entrySize(false, parent.entries[sep].key)
	}
	if donor.encoded()-lose < minIndexFill {
		return false
	}
	return receiver.encoded()+gain <= PageSize-nodeBody
}

func canMerge(left, right *ixNode, sep []byte) bool {
	extra := 0
	if !left.leaf {
		extra = entrySize(false, sep)
	}
	return left.encoded()+right.encoded()+extra <= PageSize-nodeBody
}

func searchEntries(entries []ixEntry, key []byte) (int, bool) {
	lo, hi := 0, len(entries)
	for lo < hi {
		mid := (lo + hi) / 2
		cmp := bytes.Compare(key, entries[mid].key)
		if cmp == 0 {
			return mid, true
		}
		if cmp < 0 {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return lo, false
}

func insertEntry(s []ixEntry, i int, e ixEntry) []ixEntry {
	s = append(s, ixEntry{})
	copy(s[i+1:], s[i:])
	s[i] = e
	return s
}

func copyEntries(s []ixEntry) []ixEntry {
	out := make([]ixEntry, len(s))
	for i, e := range s {
		out[i] = ixEntry{key: append([]byte(nil), e.key...), page: e.page, slot: e.slot}
	}
	return out
}
