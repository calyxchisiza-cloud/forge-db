package storage

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

func TestIndexPutGetAndReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := CreateIndex(pool)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{"m", "a", "z", "b", ""}
	for i, k := range keys {
		if err := ix.Put([]byte(k), RowID{Page: uint32(i + 1), Slot: uint16(i)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := ix.Put([]byte("a"), RowID{}); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("duplicate %v", err)
	}
	if _, err := ix.Get([]byte("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing %v", err)
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	if err := expectKeys(ix, []string{"", "a", "b", "m", "z"}); err != nil {
		t.Fatal(err)
	}
	meta := ix.MetaID()
	if err := pool.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pool, err = NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	ix, err = OpenIndex(pool, meta)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ix.Get([]byte("z"))
	if err != nil {
		t.Fatal(err)
	}
	if got.Page != 3 || got.Slot != 2 {
		t.Fatalf("row %+v", got)
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
}

func TestIndexSplitsBorrowsAndMerges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := CreateIndex(pool)
	if err != nil {
		t.Fatal(err)
	}
	const n = 800
	order := perm(n)
	for _, i := range order {
		key := keyOf(i)
		if err := ix.Put(key, RowID{Page: uint32(i + 1), Slot: uint16(i % 7)}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	for step, i := range order {
		if _, err := ix.Get(keyOf(i)); err != nil {
			t.Fatalf("get %d: %v", i, err)
		}
		if step%2 == 0 {
			continue
		}
		if err := ix.Delete(keyOf(i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
		if _, err := ix.Get(keyOf(i)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("still present %d: %v", i, err)
		}
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	for step, i := range order {
		if step%2 != 0 {
			continue
		}
		if err := ix.Delete(keyOf(i)); err != nil {
			t.Fatalf("delete rest %d: %v", i, err)
		}
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	root, err := ix.readNode(ix.root)
	if err != nil {
		t.Fatal(err)
	}
	if !root.leaf || root.parent != 0 || len(root.entries) != 0 {
		t.Fatalf("root leaf=%v parent=%d entries=%d", root.leaf, root.parent, len(root.entries))
	}
	if err := pool.Flush(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
}

func TestIndexInternalSplitAndMerge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := CreateIndex(pool)
	if err != nil {
		t.Fatal(err)
	}
	const n = 500
	for _, i := range perm(n) {
		if err := ix.Put(wideKey(i), RowID{Page: uint32(i + 1), Slot: 1}); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	root, err := ix.readNode(ix.root)
	if err != nil {
		t.Fatal(err)
	}
	if root.leaf {
		t.Fatal("root stayed a leaf")
	}
	for i := n - 1; i >= 0; i-- {
		if err := ix.Delete(wideKey(i)); err != nil {
			t.Fatalf("delete %d: %v", i, err)
		}
		if i%50 == 0 {
			if err := ix.Check(); err != nil {
				t.Fatalf("check at %d: %v", i, err)
			}
		}
	}
	if err := ix.Check(); err != nil {
		t.Fatal(err)
	}
	root, err = ix.readNode(ix.root)
	if err != nil {
		t.Fatal(err)
	}
	if !root.leaf || len(root.entries) != 0 {
		t.Fatalf("root leaf=%v entries=%d", root.leaf, len(root.entries))
	}
}

func TestIndexRejectsAHugeKey(t *testing.T) {
	pool := newIndexPool(t)
	ix, err := CreateIndex(pool)
	if err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte("k"), maxKeyBytes+1)
	if err := ix.Put(key, RowID{Page: 1}); !errors.Is(err, ErrKeyTooLarge) {
		t.Fatalf("error %v", err)
	}
	key = bytes.Repeat([]byte("k"), maxKeyBytes)
	if err := ix.Put(key, RowID{Page: 1, Slot: 4}); err != nil {
		t.Fatal(err)
	}
	got, err := ix.Get(key)
	if err != nil {
		t.Fatal(err)
	}
	if got.Slot != 4 {
		t.Fatalf("slot %d", got.Slot)
	}
}

func newIndexPool(t *testing.T) *Pool {
	t.Helper()
	p := newTestPager(t)
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func expectKeys(ix *Index, want []string) error {
	var got []string
	err := ix.Scan(func(key []byte, _ RowID) error {
		got = append(got, string(key))
		return nil
	})
	if err != nil {
		return err
	}
	if len(got) != len(want) {
		return fmt.Errorf("keys %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			return fmt.Errorf("keys %q", got)
		}
	}
	return nil
}

func keyOf(i int) []byte {
	return []byte(fmt.Sprintf("k%04d", i))
}

func wideKey(i int) []byte {
	n := 32
	if i%4 == 0 {
		n = 200
	}
	buf := bytes.Repeat([]byte{'x'}, n)
	buf[0] = byte(i)
	buf[1] = byte(i >> 8)
	return buf
}

func perm(n int) []int {
	out := make([]int, n)
	for i := range out {
		out[i] = i
	}
	for i := n - 1; i > 0; i-- {
		j := (i*17 + 3) % (i + 1)
		out[i], out[j] = out[j], out[i]
	}
	return out
}
