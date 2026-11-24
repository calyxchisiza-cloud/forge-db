package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellCreatesQueriesAndQuits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	script := strings.Join([]string{
		"CREATE TABLE account (id INTEGER PRIMARY KEY, name TEXT);",
		"INSERT INTO account VALUES (1, 'Ada');",
		"SELECT name FROM account WHERE id = 1;",
		".quit",
		"INSERT INTO account VALUES (2, 'Bea');",
	}, "\n")
	out, errOut := runScript(t, path, script)
	if !strings.Contains(out, "changed 1") || !strings.Contains(out, "Ada") {
		t.Fatalf("out %q", out)
	}
	if strings.Contains(out, "Bea") {
		t.Fatalf("quit kept reading %q", out)
	}
	if errOut != "" {
		t.Fatalf("stderr %q", errOut)
	}

	out, errOut = runScript(t, path, "SELECT name FROM account WHERE id = 1;\n")
	if !strings.Contains(out, "Ada") {
		t.Fatalf("reopen %q", out)
	}
	if errOut != "" {
		t.Fatalf("stderr %q", errOut)
	}
}

func TestShellContinuesAfterABadStatement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forge.db")
	script := strings.Join([]string{
		"CREATE TABLE account (id INTEGER PRIMARY KEY, balance INTEGER);",
		"SELECT",
		"INSERT INTO account VALUES (1, 10);",
		"SELECT balance FROM account WHERE id = 1;",
	}, "\n")
	out, errOut := runScript(t, path, script)
	if !strings.Contains(errOut, "syntax") {
		t.Fatalf("stderr %q", errOut)
	}
	if !strings.Contains(out, "10") {
		t.Fatalf("out %q", out)
	}
}

func runScript(t *testing.T, path, script string) (string, string) {
	t.Helper()
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var out, errOut bytes.Buffer
	if err := run(db, strings.NewReader(script), &out, &errOut, false); err != nil {
		t.Fatal(err)
	}
	return out.String(), errOut.String()
}
