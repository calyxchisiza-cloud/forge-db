# ForgeDB

Phase 3 stores rows in slotted pages and keeps a fixed number of those pages in a buffer pool. Page 0 is the file header. A table header page records the columns. Each row is an integer or a byte string, and either may be null.

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

Reads pin a cached page. A pinned page is not evicted. A changed page stays dirty until `Flush`, `Close`, or until its frame is needed for another page. The pager still checks checksums, page numbers, and the free list. Indexes, SQL, and the log are later phases. See [docs/architecture.md](docs/architecture.md) and [docs/storage-format.md](docs/storage-format.md).

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
