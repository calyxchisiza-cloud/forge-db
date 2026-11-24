# Storage format

Format version 1. A newer version is rejected.

## Page

Every page is 4096 bytes.

| Offset | Size | Field |
| --- | --- | --- |
| 0 | 4 | Page identifier |
| 4 | 2 | Page type |
| 6 | 2 | Reserved, zero |
| 8 | 8 | Log sequence number, zero until the log exists |
| 16 | 4 | CRC-32 of the page with this field set to zero |
| 20 | 4 | Reserved, zero |
| 24 | 4072 | Payload |

Page types: `1` file header, `2` free, `3` data.

## File header

The file header is the payload of page 0.

| Offset | Size | Field |
| --- | --- | --- |
| 24 | 4 | Magic `FGDB` |
| 28 | 4 | Format version `1` |
| 32 | 4 | Page size `4096` |
| 36 | 4 | Page count, including page 0 |
| 40 | 4 | Free-list head. Zero means the list is empty |
| 44 | 8 | Next log sequence number. It starts at 1 |

The file size is the page count times 4096. A short or trailing partial page makes the file unreadable.

## Free page

The first four payload bytes are the next free page identifier. Zero ends the list. The rest of a free page is zero. Opening the file walks the list and rejects a cycle, a missing page, or a link to a page that is not free.
