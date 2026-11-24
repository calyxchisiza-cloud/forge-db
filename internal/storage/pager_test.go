package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestHelloSurvivesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if pg.ID() != 1 {
		t.Fatalf("page id %d", pg.ID())
	}
	copy(pg.Payload(), []byte("Hello ForgeDB"))
	if err := p.Write(pg); err != nil {
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
	got, err := p.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got.Payload(), []byte("Hello ForgeDB")) {
		t.Fatalf("payload %q", got.Payload()[:13])
	}
}

func TestChecksumRejectsAFlippedByte(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pg, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("Hello ForgeDB"))
	if err := p.Write(pg); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	flipByte(t, path, int64(pg.ID())*PageSize+PageSize-1)
	p, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if _, err := p.Read(pg.ID()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error %v", err)
	}
}

func TestTornFileIsRejected(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allocate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, info.Size()-10); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("error %v", err)
	}
}

func TestPageIDMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.f.WriteAt(second.data, int64(first.ID())*PageSize); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Read(first.ID()); !errors.Is(err, ErrPageIDMismatch) {
		t.Fatalf("error %v", err)
	}
	p.Close()
}

func TestFreeReuseAndRejectHeader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allocate(); err != nil {
		t.Fatal(err)
	}
	if err := p.Free(0); !errors.Is(err, ErrFreeFileHeader) {
		t.Fatalf("free header %v", err)
	}
	if err := p.Free(first.ID()); err != nil {
		t.Fatal(err)
	}
	if err := p.Free(first.ID()); !errors.Is(err, ErrDoubleFree) {
		t.Fatalf("double free %v", err)
	}
	if err := p.Write(first); !errors.Is(err, ErrFreedPage) {
		t.Fatalf("write freed %v", err)
	}
	reused, err := p.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	if reused.ID() != first.ID() {
		t.Fatalf("reused %d want %d", reused.ID(), first.ID())
	}
	copy(reused.Payload(), []byte("again"))
	if err := p.Write(reused); err != nil {
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
	got, err := p.Read(reused.ID())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got.Payload(), []byte("again")) {
		t.Fatalf("payload %q", got.Payload()[:5])
	}
	if _, err := p.Read(99); !errors.Is(err, ErrPageRange) {
		t.Fatalf("range %v", err)
	}
}

func TestManyPagesReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	const n = 40
	for i := 0; i < n; i++ {
		pg, err := p.Allocate()
		if err != nil {
			t.Fatal(err)
		}
		pg.Payload()[0] = byte(i)
		if err := p.Write(pg); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if p.PageCount() != n+1 {
		t.Fatalf("count %d", p.PageCount())
	}
	got, err := p.Read(n)
	if err != nil {
		t.Fatal(err)
	}
	if got.Payload()[0] != byte(n-1) {
		t.Fatalf("last byte %d", got.Payload()[0])
	}
}

func TestCreateRejectsAnExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if _, err := Create(path); err == nil {
		t.Fatal("create replaced an existing file")
	}
}

func TestClosedPagerRejectsUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Allocate(); !errors.Is(err, ErrClosed) {
		t.Fatalf("error %v", err)
	}
}

func flipByte(t *testing.T, path string, off int64) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var b [1]byte
	if _, err := f.ReadAt(b[:], off); err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if _, err := f.WriteAt(b[:], off); err != nil {
		t.Fatal(err)
	}
}
