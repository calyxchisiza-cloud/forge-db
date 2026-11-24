# ForgeDB

Phase 1 is a pager. A database file is a sequence of 4 KB pages. Page 0 is the file header. Other pages hold caller bytes. Each page carries a checksum and its own page number.

```go
db, err := forgedb.Create(path)
page, err := db.Allocate()
copy(page.Payload(), []byte("Hello ForgeDB"))
err = db.Write(page)
db.Close()

db, err = forgedb.Open(path)
page, err = db.Read(1)
```

`Close` flushes the file. Reopening checks the header, every free-list page, and each page checksum. A torn file, a bad checksum, or a page stored in the wrong slot is rejected. `Free` returns a page to a free list. The file header cannot be freed. The next `Allocate` reuses a free page before the file grows.

Records, a buffer pool, indexes, SQL, and the log are later phases. See [docs/architecture.md](docs/architecture.md) and [docs/storage-format.md](docs/storage-format.md).

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
