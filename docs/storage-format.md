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

Page types: `1` file header, `2` free, `3` data, `4` slotted, `5` table header.

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

## Table header

A table header is page type 5. Its payload starts at offset 24.

| Offset | Size | Field |
| --- | --- | --- |
| 24 | 2 | Column count |
| 26 | 4 | First slotted page, or zero |
| 30 | 4 | Last slotted page, or zero |
| 34 |  | Columns: type byte, name length byte, name bytes |

Column type `1` is an int64. Column type `2` is text. A name is 1 to 64 bytes.

## Slotted page

A slotted page is page type 4.

| Offset | Size | Field |
| --- | --- | --- |
| 24 | 4 | Next slotted page, or zero |
| 28 | 2 | Slot count, including empty slots |
| 30 | 2 | First free byte for a new record |
| 32 |  | Record bytes, growing forward |

The slot directory grows backward from the end of the page. Each slot is a 2-byte offset and a 2-byte length. Length zero means the slot is empty.

A record begins with a null bitmap, one bit per column. Set means null. A live integer is 8 bytes. A live text value is a 4-byte length followed by that many bytes.

## Free page

The first four payload bytes are the next free page identifier. Zero ends the list. The rest of a free page is zero. Opening the file walks the list and rejects a cycle, a missing page, or a link to a page that is not free.
