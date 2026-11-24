# ForgeDB architecture

Phase 3 keeps pages in a fixed buffer pool. Rows still live in slotted pages, and the file is still a sequence of 4 KB pages. One caller uses the database. Later phases add a B+Tree, SQL, and a write-ahead log.

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
| Rows | `INTEGER` is an int64. `TEXT` is raw bytes. A null bitmap precedes the values. A row must fit in one page. |
| Slots | Records grow forward. The slot directory grows backward. A deleted slot stays empty and can be reused. |
| Pool | A fixed number of frames. Fetch pins a page. A pinned page stays resident. A dirty page is written before its frame is reused and on Flush. |

## Later

- Phase 4: a B+Tree that maps keys to the page and slot of a row.
- Phase 5: SQL for one table at a time, without joins or expressions.
- Phase 6: a write-ahead log of full page images, commit, rollback, and recovery.
- Phase 7: many readers and one writer.
- Phase 8: benchmarks, a statement shell, and the v1 notes.
