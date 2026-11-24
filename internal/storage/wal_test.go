package storage

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCommitWaitsInTheLogUntilOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Begin(); err != nil {
		t.Fatal(err)
	}
	pg, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("Hello"))
	if err := pool.Commit(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != PageSize {
		t.Fatalf("database file grew to %d before checkpoint", info.Size())
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
	if !bytes.HasPrefix(got.Payload(), []byte("Hello")) {
		t.Fatalf("payload %q", got.Payload()[:5])
	}
}

func TestRollbackDropsPrivatePages(t *testing.T) {
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
	if err := pool.Begin(); err != nil {
		t.Fatal(err)
	}
	pg, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("Gone"))
	if err := pool.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := pool.Commit(); !errors.Is(err, ErrNoTxn) {
		t.Fatalf("commit %v", err)
	}
	if err := pool.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	if p.PageCount() != 1 {
		t.Fatalf("pages %d", p.PageCount())
	}
}

func TestTornTailAndMissingCommitAreIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, _ := commitHello(t, path)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(walPath(path), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{1, 2, 3, 4, 5}); err != nil {
		t.Fatal(err)
	}
	f.Close()
	payload := make([]byte, pageRecSize)
	payload[0] = recPage
	payload[17] = 0
	payload[18] = 0
	payload[19] = 0
	payload[20] = 9
	extra := appendRecord(nil, payload)
	f, err = os.OpenFile(walPath(path), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(extra[:len(extra)-3]); err != nil {
		t.Fatal(err)
	}
	f.Close()
	p, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	got, err := p.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(got.Payload(), []byte("Hello")) {
		t.Fatalf("payload %q", got.Payload()[:5])
	}
	if p.PageCount() != 2 {
		t.Fatalf("pages %d", p.PageCount())
	}
}

func TestBadChecksumInTheMiddleStopsRecovery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, _ := commitHello(t, path)
	p.Close()
	body := appendRecord(nil, []byte{recCommit, 1, 2, 3, 4})
	body[6] ^= 0xff
	body = append(body, 9)
	f, err := os.OpenFile(walPath(path), os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(body); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := Open(path); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("open %v", err)
	}
}

func TestAppliedPageLSNIsNotReplayed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	p, pool := commitHello(t, path)
	saved, err := os.ReadFile(walPath(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) == 0 {
		t.Fatal("log is empty")
	}
	if err := pool.Checkpoint(); err != nil {
		t.Fatal(err)
	}
	pg, err := p.Read(1)
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("ZZZZZ"))
	putChecksum(pg.data)
	if _, err := p.f.WriteAt(pg.data, PageSize); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(walPath(path), saved, 0o644); err != nil {
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
	if !bytes.HasPrefix(got.Payload(), []byte("ZZZZZ")) {
		t.Fatalf("payload %q", got.Payload()[:5])
	}
}

func commitHello(t *testing.T, path string) (*Pager, *Pool) {
	t.Helper()
	p, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	pool, err := NewPool(p, 4)
	if err != nil {
		t.Fatal(err)
	}
	if err := pool.Begin(); err != nil {
		t.Fatal(err)
	}
	pg, err := pool.Allocate()
	if err != nil {
		t.Fatal(err)
	}
	copy(pg.Payload(), []byte("Hello"))
	if err := pool.Commit(); err != nil {
		t.Fatal(err)
	}
	return p, pool
}
