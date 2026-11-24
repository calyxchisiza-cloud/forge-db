# ForgeDB architecture

v1 keeps a transaction's pages private until commit. Commit appends those page images and a commit record to a log, and syncs the log before it returns. Opening the file replays committed images. Queries share one read lock. A writer holds the lock from BEGIN until COMMIT or ROLLBACK, so readers wait and then see the last committed pages. The statement list and the pieces left out are in [v1.md](v1.md).

## Now

| Piece | Behavior |
| --- | --- |
| File | One database is one file. `Create` refuses to replace an existing file. |
| Pages | Every page is 4096 bytes. Integers are big-endian. |
| Header | Page 0 holds the magic, format version, page size, page count, free-list head, and the next log sequence number. |
| Checks | A page stores CRC-32 of its bytes with the checksum field set to zero. The stored page number must match the slot that was read. |
| Allocate | A free page is reused. Otherwise the file grows by one page. |
| Free | The page is zeroed, marked free, and linked at the head of the free list. Page 0 stays allocated. |
| Durability | Commit syncs the log before it returns. The database file receives those pages at checkpoint, eviction, or the next open. A transaction with no commit record is discarded. |
| Rows | `INTEGER` is an int64. `TEXT` is raw bytes. A null bitmap precedes the values. A row must fit in one page. |
| Slots | Records grow forward. The slot directory grows backward. A deleted slot stays empty and can be reused. |
| Pool | A fixed number of frames. Fetch pins a page. A pinned page stays resident. A dirty page is written before its frame is reused and on Flush. |
| Index | A B+Tree of byte keys. Each key points at a page and slot. Delete borrows from a sibling or merges, and the tree can be checked. |
| SQL | `CREATE TABLE`, `INSERT`, `SELECT`, `UPDATE`, and `DELETE` use literal values. One column is the primary key. A comparison with NULL is not true. A statement outside `BEGIN` commits on success and rolls back on failure. |
| Lock | Many queries run together. One writer waits until they finish, then keeps the lock until commit or rollback. A failed statement ends that transaction and releases the writer. |
| Shell | `cmd/shell` runs one statement per line. A query prints rows. Any other statement prints how many rows changed. |
| Page | `cmd/ui` lists every database file, then shows that file's tables, keys, and rows. |
