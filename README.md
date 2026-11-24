# ForgeDB

Phase 7 stores rows in slotted pages, keeps pages in a buffer pool, maps primary keys with a B+Tree, and records committed pages in a log. Page 0 is the file header. A table has one primary-key column. Each other value is an integer or a byte string, and either may be null. Queries run together. A writer waits, then holds the database until commit or rollback.

```go
db, err := forgedb.Create(path)
page, err := db.Allocate()
copy(page.Payload(), []byte("Hello ForgeDB"))
err = db.Write(page)
db.Close()

db, err = forgedb.Open(path)
page, err = db.Read(1)
```

`CreateTable` stores the column list. `Insert`, `Get`, `Update`, `Delete`, and `Scan` use a page number and a slot. A delete leaves that slot empty so later rows keep their identifiers. An update that no longer fits on the page moves to another page and returns the new identifier. A row larger than one page is rejected. Reopening the file reads the same rows.

Reads pin a cached page. A pinned page is not evicted. A changed page stays dirty until `Flush`, `Close`, or until its frame is needed for another page. `Exec` accepts `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, and `DELETE`. Values are literals, so a transfer is two statements with the new balances already computed. A comparison with NULL matches nothing. `BEGIN`, `COMMIT`, and `ROLLBACK` group statements. A failed statement rolls the transaction back and releases the writer. Several `SELECT`s can run at once. A writer waits for them, and a query waits while a transaction is open. Commit syncs `path.wal` before it returns; opening the database replays that log. See [docs/architecture.md](docs/architecture.md) and [docs/storage-format.md](docs/storage-format.md).

## Run

```
go run ./cmd/demo
```

The demo prints `Hello ForgeDB`.

## Test

```
go test ./...
go test -race ./...
```
