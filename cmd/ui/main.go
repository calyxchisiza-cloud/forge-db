// Command ui serves a page that shows one ForgeDB file.
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"unicode"
	"unicode/utf8"

	"forgedb"
)

//go:embed view.html
var page embed.FS

func main() {
	log.SetFlags(0)
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	path := flag.String("db", "", "database file")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: ui -db file")
		os.Exit(2)
	}
	db, err := openDB(*path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	srv := &http.Server{Addr: *addr, Handler: handler(db)}
	log.Printf("ForgeDB viewer at http://%s", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func openDB(path string) (*forgedb.DB, error) {
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return forgedb.Create(path)
	}
	if err != nil {
		return nil, err
	}
	return forgedb.Open(path)
}

func handler(db *forgedb.DB) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", servePage)
	mux.HandleFunc("/api/inspect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		snap, err := db.Inspect()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, inspectJSON(snap))
	})
	mux.HandleFunc("/api/rows", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		name := r.URL.Query().Get("table")
		if !safeIdent(name) {
			writeErr(w, http.StatusBadRequest, errors.New("table name is not an identifier"))
			return
		}
		res, err := db.Exec("SELECT * FROM " + name + " LIMIT 200")
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, resultJSON(res))
	})
	mux.HandleFunc("/api/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		var req struct {
			Statement string `json:"statement"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if req.Statement == "" {
			writeErr(w, http.StatusBadRequest, errors.New("statement is empty"))
			return
		}
		res, err := db.Exec(req.Statement)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, resultJSON(res))
	})
	return mux
}

func servePage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	b, err := page.ReadFile("view.html")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(b)
}

type inspectBody struct {
	Path      string      `json:"path"`
	PageSize  int         `json:"pageSize"`
	PageCount uint32      `json:"pageCount"`
	FreeHead  uint32      `json:"freeHead"`
	NextLSN   uint64      `json:"nextLSN"`
	Catalog   uint32      `json:"catalog"`
	WALBytes  int64       `json:"walBytes"`
	Tables    []tableBody `json:"tables"`
	Pages     []pageBody  `json:"pages"`
	Truncated bool        `json:"truncated"`
}

type tableBody struct {
	Name       string       `json:"name"`
	Header     uint32       `json:"header"`
	Index      uint32       `json:"index"`
	PrimaryKey string       `json:"primaryKey"`
	Columns    []columnBody `json:"columns"`
}

type columnBody struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Primary bool   `json:"primary"`
}

type pageBody struct {
	ID   uint32 `json:"id"`
	Kind string `json:"kind"`
	LSN  uint64 `json:"lsn"`
}

type resultBody struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Changed int      `json:"changed"`
}

func inspectJSON(snap *forgedb.Snapshot) inspectBody {
	out := inspectBody{
		Path:      snap.Path,
		PageSize:  snap.PageSize,
		PageCount: snap.PageCount,
		FreeHead:  snap.FreeHead,
		NextLSN:   snap.NextLSN,
		Catalog:   snap.Catalog,
		WALBytes:  snap.WALBytes,
		Tables:    make([]tableBody, len(snap.Tables)),
		Pages:     make([]pageBody, len(snap.Pages)),
		Truncated: snap.Truncated,
	}
	for i, table := range snap.Tables {
		cols := make([]columnBody, len(table.Columns))
		for j, col := range table.Columns {
			cols[j] = columnBody{Name: col.Name, Type: col.Type, Primary: col.Primary}
		}
		out.Tables[i] = tableBody{
			Name: table.Name, Header: table.Header, Index: table.Index,
			PrimaryKey: table.PrimaryKey, Columns: cols,
		}
	}
	for i, pg := range snap.Pages {
		out.Pages[i] = pageBody{ID: pg.ID, Kind: pg.Kind, LSN: pg.LSN}
	}
	return out
}

func resultJSON(res *forgedb.Result) resultBody {
	out := resultBody{
		Columns: append([]string(nil), res.Columns...),
		Rows:    make([][]any, len(res.Rows)),
		Changed: res.Changed,
	}
	for i, row := range res.Rows {
		out.Rows[i] = make([]any, len(row))
		for j, val := range row {
			out.Rows[i][j] = cellJSON(val)
		}
	}
	return out
}

func cellJSON(v forgedb.Value) any {
	if v.Null {
		return nil
	}
	if v.Text != nil {
		return string(v.Text)
	}
	return v.Int
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func safeIdent(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for i, r := range name {
		if r == '_' || unicode.IsLetter(r) {
			continue
		}
		if i > 0 && unicode.IsDigit(r) {
			continue
		}
		return false
	}
	r, _ := utf8.DecodeRuneInString(name)
	return r != utf8.RuneError
}
