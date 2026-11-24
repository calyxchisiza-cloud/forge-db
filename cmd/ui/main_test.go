package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestViewerShowsATableAndRunsAStatement(t *testing.T) {
	db, err := openDB(filepath.Join(t.TempDir(), "forge.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE account (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO account VALUES (1, 'Ada')`); err != nil {
		t.Fatal(err)
	}
	h := handler(db)

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "Statement") {
		t.Fatalf("page %d %s", res.Code, res.Body.String())
	}

	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/inspect", nil))
	var snap inspectBody
	if err := json.Unmarshal(res.Body.Bytes(), &snap); err != nil {
		t.Fatal(err)
	}
	if len(snap.Tables) != 1 || snap.Tables[0].Name != "account" || snap.PageCount < 2 {
		t.Fatalf("inspect %+v", snap)
	}

	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/rows?table=account", nil))
	var rows resultBody
	if err := json.Unmarshal(res.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows.Rows) != 1 || rows.Rows[0][1] != "Ada" {
		t.Fatalf("rows %+v", rows.Rows)
	}

	res = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/exec", strings.NewReader(`{"statement":"INSERT INTO account VALUES (2, 'Bea')"}`))
	h.ServeHTTP(res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("exec %d %s", res.Code, res.Body.String())
	}
	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/rows?table=account%20x", nil))
	if res.Code != http.StatusBadRequest {
		t.Fatalf("bad name %d", res.Code)
	}
}
