package forgedb

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestReadersDoNotExcludeEachOther(t *testing.T) {
	db := openConcDB(t)
	release := make(chan struct{})
	started := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db.pool.LockRead()
			started <- struct{}{}
			<-release
			db.pool.UnlockRead()
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(2 * time.Second):
			t.Fatal("reader did not enter")
		}
	}
	blocked := make(chan struct{})
	go func() {
		db.pool.LockWrite()
		close(blocked)
		db.pool.UnlockWrite()
	}()
	select {
	case <-blocked:
		t.Fatal("writer ran beside readers")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-blocked:
	case <-time.After(2 * time.Second):
		t.Fatal("writer did not enter after readers left")
	}
	wg.Wait()
}

func TestWriterHidesUncommittedRows(t *testing.T) {
	db := openConcDB(t)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE account SET balance = 99 WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	saw := make(chan int64, 1)
	go func() {
		res, err := db.Exec(`SELECT balance FROM account WHERE id = 1`)
		if err != nil {
			saw <- -1
			return
		}
		saw <- res.Rows[0][0].Int
	}()
	select {
	case got := <-saw:
		t.Fatalf("reader saw %d before commit", got)
	case <-time.After(150 * time.Millisecond):
	}
	if _, err := db.Exec(`COMMIT`); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-saw:
		if got != 99 {
			t.Fatalf("balance %d", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader did not finish after commit")
	}
}

func TestFailedStatementReleasesTheWriter(t *testing.T) {
	db := openConcDB(t)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 10)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`BEGIN`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 11)`); err == nil {
		t.Fatal("duplicate inserted")
	}
	done := make(chan error, 1)
	go func() {
		_, err := db.Exec(`SELECT id FROM account WHERE id = 1`)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reader waited after the failed statement")
	}
}

func TestWritersDoNotLoseUpdates(t *testing.T) {
	db := openConcDB(t)
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 0)`); err != nil {
		t.Fatal(err)
	}
	const writers = 4
	const each = 20
	errc := make(chan error, writers)
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < each; n++ {
				if _, err := db.Exec(`BEGIN`); err != nil {
					errc <- err
					return
				}
				res, err := db.Exec(`SELECT balance FROM account WHERE id = 1`)
				if err != nil {
					errc <- err
					return
				}
				next := res.Rows[0][0].Int + 1
				_, err = db.Exec(fmt.Sprintf(`UPDATE account SET balance = %d WHERE id = 1`, next))
				if err != nil {
					errc <- err
					return
				}
				if _, err := db.Exec(`COMMIT`); err != nil {
					errc <- err
					return
				}
			}
			errc <- nil
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Fatal(err)
		}
	}
	res, err := db.Exec(`SELECT balance FROM account WHERE id = 1`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows[0][0].Int != writers*each {
		t.Fatalf("balance %d", res.Rows[0][0].Int)
	}
}

func openConcDB(t *testing.T) *DB {
	t.Helper()
	db, err := Create(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}
