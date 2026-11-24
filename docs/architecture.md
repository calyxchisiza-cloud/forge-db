# ForgeDB architecture

Phase 5 runs one SQL statement at a time. A B+Tree stores each primary key, rows live in slotted pages, and cached pages stay in a fixed buffer pool. One caller uses the database. Later phases add a write-ahead log and concurrent readers.

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
| Index | A B+Tree of byte keys. Each key points at a page and slot. Delete borrows from a sibling or merges, and the tree can be checked. |
| SQL | `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, and `DELETE` use literal values. One column is the primary key. A comparison with NULL is not true. |

## Later

- Phase 6: a write-ahead log of full page images, commit, rollback, and recovery.
- Phase 7: many readers and one writer.
- Phase 8: benchmarks, a statement shell, and the v1 notes.
