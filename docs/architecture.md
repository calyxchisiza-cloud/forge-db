# ForgeDB architecture

Phase 1 stores a database as fixed-size pages in one file. One caller uses the pager. Later phases add records, a buffer pool, a B+Tree, SQL, and a write-ahead log.

## Now

| Piece | Behavior |
| --- | --- |
| File | One database is one file. `Create` refuses to replace an existing file. |
| Pages | Every page is 4096 bytes. Integers are big-endian. |
| Header | Page 0 holds the magic, format version, page size, page count, free-list head, and the next log sequence number. |
| Checks | A page stores CRC-32 of its bytes with the checksum field set to zero. The stored page number must match the slot that was read. |
| Allocate | A free page is reused. Otherwise the file grows by one page. |
| Free | The page is zeroed, marked free, and linked at the head of the free list. Page 0 stays allocated. |
| Durability | `Flush` and `Close` call `File.Sync`. A crash between two writes is recovered once the log exists. |

## Later

- Phase 2: slotted pages, `INTEGER` and `TEXT` rows, and insert, get, update, delete, and scan.
- Phase 3: a buffer pool with pin counts, dirty pages, and LRU replacement.
- Phase 4: a B+Tree that maps keys to the page and slot of a row.
- Phase 5: SQL for one table at a time, without joins or expressions.
- Phase 6: a write-ahead log of full page images, commit, rollback, and recovery.
- Phase 7: many readers and one writer.
- Phase 8: benchmarks, a statement shell, and the v1 notes.
