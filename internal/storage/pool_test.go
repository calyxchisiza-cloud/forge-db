package storage

import (
	"bytes"
	"errors"
	"testing"
)

func TestPinnedPageStaysResident(t *testing.T) {
	p := newTestPager(t)
	pool, err := NewPool(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := writeUserPage(t, p, "one")
	second := writeUserPage(t, p, "two")
	if _, err := pool.Fetch(first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Fetch(second); !errors.Is(err, ErrPoolFull) {
		t.Fatalf("error %v", err)
	}
	if err := pool.Unpin(first); err != nil {
		t.Fatal(err)
	}
	got, err := pool.Fetch(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got.Payload(), []byte("two")) {
		t.Fatalf("payload %q", got.Payload()[:3])
	}
	pool.Unpin(second)
}

func TestDirtyPageIsWrittenWhenEvicted(t *testing.T) {
	p := newTestPager(t)
	pool, err := NewPool(p, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := writeUserPage(t, p, "one")
	second := writeUserPage(t, p, "two")
	pg, err := pool.Fetch(first)
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("dirty-page"))
	putChecksum(pg.data)
	if err := pool.MarkDirty(first); err != nil {
		t.Fatal(err)
	}
	if err := pool.Unpin(first); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Fetch(second); err != nil {
		t.Fatal(err)
	}
	pool.Unpin(second)
	got, err := p.Read(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got.Payload(), []byte("dirty-page")) {
		t.Fatalf("payload %q", got.Payload()[:10])
	}
	if pool.Stats().Evictions != 1 || pool.Stats().Flushes != 1 {
		t.Fatalf("stats %+v", pool.Stats())
	}
}

func TestLeastRecentlyUsedFrameIsEvicted(t *testing.T) {
	p := newTestPager(t)
	pool, err := NewPool(p, 2)
	if err != nil {
		t.Fatal(err)
	}
	a := writeUserPage(t, p, "A")
	b := writeUserPage(t, p, "B")
	c := writeUserPage(t, p, "C")
	for _, id := range []uint32{a, b} {
		if _, err := pool.Fetch(id); err != nil {
			t.Fatal(err)
		}
		if err := pool.Unpin(id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Fetch(a); err != nil {
		t.Fatal(err)
	}
	pool.Unpin(a)
	if _, err := pool.Fetch(c); err != nil {
		t.Fatal(err)
	}
	pool.Unpin(c)
	before := pool.Stats().Hits
	if _, err := pool.Fetch(a); err != nil {
		t.Fatal(err)
	}
	pool.Unpin(a)
	if pool.Stats().Hits != before+1 {
		t.Fatal("the recently used page was evicted")
	}
	if _, err := pool.Fetch(b); err != nil {
		t.Fatal(err)
	}
	pool.Unpin(b)
	if pool.Stats().Hits != before+1 {
		t.Fatal("the least recently used page was still cached")
	}
}

func writeUserPage(t *testing.T, p *Pager, text string) uint32 {
	t.Helper()
	pg, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), text)
	if err := p.Write(pg); err != nil {
		t.Fatal(err)
	}
	return pg.ID()
}
