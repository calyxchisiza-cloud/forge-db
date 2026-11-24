// Command ui serves a page for creating and editing ForgeDB files.
package main

import (
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"forgedb"
)

//go:embed view.html
var page embed.FS

func main() {
	log.SetFlags(0)
	addr := flag.String("addr", "127.0.0.1:8091", "listen address")
	dir := flag.String("dir", ".", "folder that holds database files")
	dbFlag := flag.String("db", "", "database file to open")
	flag.Parse()
	lib, err := newLibrary(*dir, *dbFlag)
	if err != nil {
		log.Fatal(err)
	}
	defer lib.close()
	srv := &http.Server{Addr: *addr, Handler: handler(lib)}
	log.Printf("ForgeDB at http://%s", *addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

type library struct {
	dir  string
	mu   sync.Mutex
	name string
	db   *forgedb.DB
}

func newLibrary(dirFlag, dbFlag string) (*library, error) {
	dir, name, err := resolve(dirFlag, dbFlag)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	lib := &library{dir: dir}
	if name != "" {
		if err := lib.open(name, true); err != nil {
			return nil, err
		}
	}
	return lib, nil
}

func resolve(dirFlag, dbFlag string) (string, string, error) {
	dir := dirFlag
	name := ""
	if dbFlag != "" {
		if filepath.Dir(dbFlag) != "." {
			dir = filepath.Dir(dbFlag)
		}
		base := filepath.Base(dbFlag)
		name = strings.TrimSuffix(base, ".db")
		if !safeIdent(name) {
			return "", "", errors.New("database name is not an identifier")
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", "", err
	}
	return abs, name, nil
}

func (l *library) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		l.db.Close()
		l.db = nil
	}
}

func (l *library) names() ([]string, error) {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) < 4 || !strings.HasSuffix(strings.ToLower(entry.Name()), ".db") {
			continue
		}
		name := entry.Name()[:len(entry.Name())-3]
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}

type dbCard struct {
	Name      string   `json:"name"`
	Bytes     int64    `json:"bytes"`
	PageCount uint32   `json:"pageCount"`
	Tables    []string `json:"tables"`
	Error     string   `json:"error,omitempty"`
}

func (l *library) catalog() (string, string, []dbCard, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	names, err := l.names()
	if err != nil {
		return "", "", nil, err
	}
	cards := make([]dbCard, 0, len(names))
	for _, name := range names {
		cards = append(cards, l.summarize(name))
	}
	return l.dir, l.name, cards, nil
}

func (l *library) summarize(name string) dbCard {
	card := dbCard{Name: name, Tables: []string{}}
	path := filepath.Join(l.dir, name+".db")
	if info, err := os.Stat(path); err == nil {
		card.Bytes = info.Size()
	}
	if !safeIdent(name) {
		card.Error = "database name is not an identifier"
		return card
	}
	var snap *forgedb.Snapshot
	var err error
	if name == l.name && l.db != nil {
		snap, err = l.db.Inspect()
	} else {
		db, openErr := forgedb.Open(path)
		if openErr != nil {
			card.Error = openErr.Error()
			return card
		}
		snap, err = db.Inspect()
		db.Close()
	}
	if err != nil {
		card.Error = err.Error()
		return card
	}
	card.PageCount = snap.PageCount
	if logical := int64(snap.PageCount) * int64(snap.PageSize); logical > card.Bytes {
		card.Bytes = logical
	}
	for _, table := range snap.Tables {
		card.Tables = append(card.Tables, table.Name)
	}
	return card
}

func (l *library) open(name string, create bool) error {
	if !safeIdent(name) {
		return errors.New("database name is not an identifier")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	path := filepath.Join(l.dir, name+".db")
	var db *forgedb.DB
	var err error
	if create {
		if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
			db, err = forgedb.Create(path)
		} else if statErr != nil {
			return statErr
		} else {
			db, err = forgedb.Open(path)
		}
	} else {
		db, err = forgedb.Open(path)
	}
	if err != nil {
		return err
	}
	if l.db != nil {
		l.db.Close()
	}
	l.db = db
	l.name = name
	return nil
}

func (l *library) leave() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db != nil {
		l.db.Close()
		l.db = nil
		l.name = ""
	}
}

func (l *library) do(fn func(*forgedb.DB) error) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.db == nil {
		return errors.New("no database is open")
	}
	return fn(l.db)
}

func handler(lib *library) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", servePage)
	mux.HandleFunc("/api/databases", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			dir, current, databases, err := lib.catalog()
			if err != nil {
				writeErr(w, http.StatusInternalServerError, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "current": current, "databases": databases})
		case http.MethodPost:
			var req struct {
				Name string `json:"name"`
			}
			if err := readJSON(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			path := filepath.Join(lib.dir, req.Name+".db")
			if _, err := os.Stat(path); err == nil {
				writeErr(w, http.StatusBadRequest, errors.New("that database already exists"))
				return
			}
			if err := lib.open(req.Name, true); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"name": req.Name})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/close", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		lib.leave()
		writeJSON(w, http.StatusOK, map[string]string{"ok": "closed"})
	})
	mux.HandleFunc("/api/open", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := lib.open(req.Name, false); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"name": req.Name})
	})
	mux.HandleFunc("/api/inspect", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var snap *forgedb.Snapshot
		err := lib.do(func(db *forgedb.DB) error {
			var err error
			snap, err = db.Inspect()
			return err
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, inspectJSON(snap))
	})
	mux.HandleFunc("/api/rows", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			name := r.URL.Query().Get("table")
			var out resultBody
			err := lib.do(func(db *forgedb.DB) error {
				res, err := queryTable(db, name)
				if err != nil {
					return err
				}
				out = resultJSON(res)
				return nil
			})
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, out)
		case http.MethodPost:
			var req struct {
				Table  string    `json:"table"`
				Values []literal `json:"values"`
			}
			if err := readJSON(r, &req); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			err := lib.do(func(db *forgedb.DB) error {
				table, err := findTable(db, req.Table)
				if err != nil {
					return err
				}
				if len(req.Values) != len(table.Columns) {
					return errors.New("supply every column")
				}
				var vals []string
				for i, col := range table.Columns {
					if col.Primary && req.Values[i].Null {
						return errors.New("the primary key cannot be null")
					}
					lit, err := req.Values[i].sql(col.Type)
					if err != nil {
						return err
					}
					vals = append(vals, lit)
				}
				_, err = db.Exec("INSERT INTO " + table.Name + " VALUES (" + strings.Join(vals, ", ") + ")")
				return err
			})
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"ok": "inserted"})
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})
	mux.HandleFunc("/api/rows/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Table  string    `json:"table"`
			Key    literal   `json:"key"`
			Values []literal `json:"values"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		err := lib.do(func(db *forgedb.DB) error {
			table, err := findTable(db, req.Table)
			if err != nil {
				return err
			}
			if len(req.Values) != len(table.Columns) {
				return errors.New("supply every column")
			}
			key, err := req.Key.sql(pkType(table))
			if err != nil {
				return err
			}
			sets := make([]string, len(table.Columns))
			for i, col := range table.Columns {
				if col.Primary && req.Values[i].Null {
					return errors.New("the primary key cannot be null")
				}
				lit, err := req.Values[i].sql(col.Type)
				if err != nil {
					return err
				}
				sets[i] = col.Name + " = " + lit
			}
			_, err = db.Exec("UPDATE " + table.Name + " SET " + strings.Join(sets, ", ") + " WHERE " + table.PrimaryKey + " = " + key)
			return err
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "updated"})
	})
	mux.HandleFunc("/api/rows/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Table string  `json:"table"`
			Key   literal `json:"key"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		err := lib.do(func(db *forgedb.DB) error {
			table, err := findTable(db, req.Table)
			if err != nil {
				return err
			}
			key, err := req.Key.sql(pkType(table))
			if err != nil {
				return err
			}
			_, err = db.Exec("DELETE FROM " + table.Name + " WHERE " + table.PrimaryKey + " = " + key)
			return err
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "deleted"})
	})
	mux.HandleFunc("/api/tables", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name    string `json:"name"`
			Columns []struct {
				Name    string `json:"name"`
				Type    string `json:"type"`
				Primary bool   `json:"primary"`
			} `json:"columns"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		stmt, err := createTableSQL(req.Name, req.Columns)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		err = lib.do(func(db *forgedb.DB) error {
			_, err := db.Exec(stmt)
			return err
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "created"})
	})
	mux.HandleFunc("/api/exec", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Statement string `json:"statement"`
		}
		if err := readJSON(r, &req); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(req.Statement) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("statement is empty"))
			return
		}
		var out resultBody
		err := lib.do(func(db *forgedb.DB) error {
			res, err := db.Exec(req.Statement)
			if err != nil {
				return err
			}
			out = resultJSON(res)
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	return mux
}

func createTableSQL(name string, cols []struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Primary bool   `json:"primary"`
}) (string, error) {
	if !safeIdent(name) {
		return "", errors.New("table name is not an identifier")
	}
	if len(cols) == 0 {
		return "", errors.New("add at least one column")
	}
	seen := map[string]bool{}
	keys := 0
	parts := make([]string, len(cols))
	for i, col := range cols {
		if !safeIdent(col.Name) {
			return "", errors.New("column name is not an identifier")
		}
		if seen[col.Name] {
			return "", errors.New("column names must be different")
		}
		seen[col.Name] = true
		kind := strings.ToUpper(col.Type)
		if kind != "INTEGER" && kind != "TEXT" {
			return "", errors.New("a column is INTEGER or TEXT")
		}
		part := col.Name + " " + kind
		if col.Primary {
			part += " PRIMARY KEY"
			keys++
		}
		parts[i] = part
	}
	if keys != 1 {
		return "", errors.New("choose one primary key")
	}
	return "CREATE TABLE " + name + " (" + strings.Join(parts, ", ") + ")", nil
}

func queryTable(db *forgedb.DB, name string) (*forgedb.Result, error) {
	if !safeIdent(name) {
		return nil, errors.New("table name is not an identifier")
	}
	return db.Exec("SELECT * FROM " + name + " LIMIT 200")
}

func findTable(db *forgedb.DB, name string) (forgedb.TableView, error) {
	if !safeIdent(name) {
		return forgedb.TableView{}, errors.New("table name is not an identifier")
	}
	snap, err := db.Inspect()
	if err != nil {
		return forgedb.TableView{}, err
	}
	for _, table := range snap.Tables {
		if table.Name == name {
			return table, nil
		}
	}
	return forgedb.TableView{}, errors.New("table was not found")
}

func pkType(table forgedb.TableView) string {
	for _, col := range table.Columns {
		if col.Primary {
			return col.Type
		}
	}
	return ""
}

type literal struct {
	Null bool    `json:"null"`
	Int  *int64  `json:"int"`
	Text *string `json:"text"`
}

func (v literal) sql(kind string) (string, error) {
	if v.Null {
		return "NULL", nil
	}
	switch kind {
	case "INTEGER":
		if v.Int == nil {
			return "", errors.New("an integer value is required")
		}
		return strconv.FormatInt(*v.Int, 10), nil
	case "TEXT":
		if v.Text == nil {
			return "", errors.New("a text value is required")
		}
		return "'" + strings.ReplaceAll(*v.Text, "'", "''") + "'", nil
	default:
		return "", errors.New("column type is not supported")
	}
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

func readJSON(r *http.Request, dest any) error {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dest); err != nil {
		return err
	}
	return nil
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
