package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestManageDatabaseFromThePage(t *testing.T) {
	lib, err := newLibrary(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer lib.close()
	h := handler(lib)

	res := httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/", nil))
	if res.Code != http.StatusOK || !strings.Contains(res.Body.String(), "New database") {
		t.Fatalf("page %d", res.Code)
	}

	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		h.ServeHTTP(rec, req)
		return rec
	}
	if rec := post("/api/databases", `{"name":"shop"}`); rec.Code != http.StatusOK {
		t.Fatalf("create db %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/tables", `{"name":"account","columns":[{"name":"id","type":"INTEGER","primary":true},{"name":"name","type":"TEXT"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("create table %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/rows", `{"table":"account","values":[{"int":1},{"text":"Ada"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("insert %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/rows", `{"table":"account","values":[{"int":2},{"text":"O'Brien"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("quote %d %s", rec.Code, rec.Body.String())
	}

	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/rows?table=account", nil))
	var rows resultBody
	if err := json.Unmarshal(res.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows.Rows) != 2 || rows.Rows[0][1] != "Ada" || rows.Rows[1][1] != "O'Brien" {
		t.Fatalf("rows %+v", rows.Rows)
	}

	if rec := post("/api/rows/update", `{"table":"account","key":{"int":1},"values":[{"int":1},{"text":"Ada Lovelace"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("update %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/rows/delete", `{"table":"account","key":{"int":2}}`); rec.Code != http.StatusOK {
		t.Fatalf("delete %d %s", rec.Code, rec.Body.String())
	}
	res = httptest.NewRecorder()
	h.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/api/rows?table=account", nil))
	if err := json.Unmarshal(res.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows.Rows) != 1 || rows.Rows[0][1] != "Ada Lovelace" {
		t.Fatalf("after edit %+v", rows.Rows)
	}
	if rec := post("/api/databases", `{"name":"shop"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("duplicate %d", rec.Code)
	}
}

func TestOpenExistingDatabaseFlag(t *testing.T) {
	dir := t.TempDir()
	lib, err := newLibrary(dir, "shop")
	if err != nil {
		t.Fatal(err)
	}
	lib.close()
	lib, err = newLibrary(dir, "shop.db")
	if err != nil {
		t.Fatal(err)
	}
	defer lib.close()
	if lib.name != "shop" {
		t.Fatalf("name %s", lib.name)
	}
}
