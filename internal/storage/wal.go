package storage

import (
	"encoding/binary"
	"hash/crc32"
	"os"
)

const (
	recPage   = 1
	recCommit = 2

	pageRecSize   = 1 + 8 + 8 + 4 + PageSize
	commitRecSize = 1 + 8 + 8 + 4
)

func walPath(path string) string { return path + ".wal" }

type walPage struct {
	txn   uint64
	lsn   uint64
	id    uint32
	image []byte
}

type walCommit struct {
	txn    uint64
	lsn    uint64
	npages uint32
}

// writeCommit appends one committed transaction and syncs the log.
func writeCommit(path string, txn uint64, pages []walPage, commitLSN uint64) error {
	buf := make([]byte, 0, (len(pages)+1)*pageRecSize)
	for _, pg := range pages {
		payload := make([]byte, pageRecSize)
		payload[0] = recPage
		binary.BigEndian.PutUint64(payload[1:], pg.txn)
		binary.BigEndian.PutUint64(payload[9:], pg.lsn)
		binary.BigEndian.PutUint32(payload[17:], pg.id)
		copy(payload[21:], pg.image)
		buf = appendRecord(buf, payload)
	}
	payload := make([]byte, commitRecSize)
	payload[0] = recCommit
	binary.BigEndian.PutUint64(payload[1:], txn)
	binary.BigEndian.PutUint64(payload[9:], commitLSN)
	binary.BigEndian.PutUint32(payload[17:], uint32(len(pages)))
	buf = appendRecord(buf, payload)
	f, err := os.OpenFile(walPath(path), os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(buf)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	return cerr
}

func appendRecord(dst, payload []byte) []byte {
	var head [8]byte
	binary.BigEndian.PutUint32(head[0:], uint32(len(payload)))
	binary.BigEndian.PutUint32(head[4:], crc32.ChecksumIEEE(payload))
	dst = append(dst, head[:]...)
	return append(dst, payload...)
}

// replayWAL writes committed page images into the database file.
// A short or bad final record is ignored. A bad checksum before the end stops recovery.
func replayWAL(db *os.File, path string) error {
	data, err := os.ReadFile(walPath(path))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(data) == 0 {
		return nil
	}
	var group []walPage
	var groupTxn uint64
	open := false
	off := 0
	for off < len(data) {
		if len(data)-off < 8 {
			return finishReplay(db, path)
		}
		n := int(binary.BigEndian.Uint32(data[off:]))
		sum := binary.BigEndian.Uint32(data[off+4:])
		if n < 0 || len(data)-off-8 < n {
			return finishReplay(db, path)
		}
		payload := data[off+8 : off+8+n]
		next := off + 8 + n
		if crc32.ChecksumIEEE(payload) != sum {
			if next == len(data) {
				return finishReplay(db, path)
			}
			_ = db.Sync()
			return ErrCorrupt
		}
		off = next
		if len(payload) == 0 {
			return ErrCorrupt
		}
		switch payload[0] {
		case recPage:
			pg, err := decodePage(payload)
			if err != nil {
				return err
			}
			if !open || groupTxn != pg.txn {
				if open {
					group = nil
				}
				open = true
				groupTxn = pg.txn
			}
			group = append(group, pg)
		case recCommit:
			cm, err := decodeCommit(payload)
			if err != nil {
				return err
			}
			if !open || groupTxn != cm.txn || uint32(len(group)) != cm.npages {
				return ErrCorrupt
			}
			for _, pg := range group {
				if err := applyPage(db, pg); err != nil {
					return err
				}
			}
			group = nil
			open = false
		default:
			return ErrCorrupt
		}
	}
	return finishReplay(db, path)
}

func finishReplay(db *os.File, path string) error {
	if err := db.Sync(); err != nil {
		return err
	}
	return os.Truncate(walPath(path), 0)
}

func decodePage(payload []byte) (walPage, error) {
	if len(payload) != pageRecSize {
		return walPage{}, ErrCorrupt
	}
	image := append([]byte(nil), payload[21:]...)
	if len(image) != PageSize || !checksumOK(image) {
		return walPage{}, ErrCorrupt
	}
	id := binary.BigEndian.Uint32(payload[17:])
	if binary.BigEndian.Uint32(image[offPageID:]) != id {
		return walPage{}, ErrCorrupt
	}
	return walPage{
		txn:   binary.BigEndian.Uint64(payload[1:]),
		lsn:   binary.BigEndian.Uint64(payload[9:]),
		id:    id,
		image: image,
	}, nil
}

func decodeCommit(payload []byte) (walCommit, error) {
	if len(payload) != commitRecSize {
		return walCommit{}, ErrCorrupt
	}
	return walCommit{
		txn:    binary.BigEndian.Uint64(payload[1:]),
		lsn:    binary.BigEndian.Uint64(payload[9:]),
		npages: binary.BigEndian.Uint32(payload[17:]),
	}, nil
}

func applyPage(db *os.File, pg walPage) error {
	info, err := db.Stat()
	if err != nil {
		return err
	}
	if info.Size() >= int64(pg.id+1)*PageSize {
		buf := make([]byte, PageSize)
		n, _ := db.ReadAt(buf, int64(pg.id)*PageSize)
		if n == PageSize && checksumOK(buf) && binary.BigEndian.Uint64(buf[offLSN:]) >= pg.lsn {
			return nil
		}
	}
	_, err = db.WriteAt(pg.image, int64(pg.id)*PageSize)
	return err
}

func truncateWAL(path string) error {
	name := walPath(path)
	if _, err := os.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return os.Truncate(name, 0)
}
