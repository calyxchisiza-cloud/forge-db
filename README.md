# ForgeDB

Phase 5 stores rows in slotted pages, keeps pages in a buffer pool, maps primary keys with a B+Tree, and runs one SQL statement at a time. Page 0 is the file header. A table has one primary-key column. Each other value is an integer or a byte string, and either may be null.

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

Reads pin a cached page. A pinned page is not evicted. A changed page stays dirty until `Flush`, `Close`, or until its frame is needed for another page. `Exec` accepts `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, and `DELETE`. Values are literals, so a transfer is two statements with the new balances already computed. A comparison with NULL matches nothing. A primary-key equality reads the B+Tree. The log is a later phase. See [docs/architecture.md](docs/architecture.md) and [docs/storage-format.md](docs/storage-format.md).

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
