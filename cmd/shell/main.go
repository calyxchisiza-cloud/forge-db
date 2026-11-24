// Command shell reads one statement per line and runs it against a database file.
package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	"forgedb"
)

func main() {
	log.SetFlags(0)
	path := flag.String("db", "", "database file")
	flag.Parse()
	if *path == "" {
		fmt.Fprintln(os.Stderr, "usage: shell -db file")
		os.Exit(2)
	}
	db, err := openDB(*path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	info, statErr := os.Stdin.Stat()
	prompt := statErr == nil && info.Mode()&os.ModeCharDevice != 0
	if err := run(db, os.Stdin, os.Stdout, os.Stderr, prompt); err != nil {
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

// run executes each input line. ".quit" or the end of input returns.
// A statement error is written to errw and the next line still runs.
func run(db *forgedb.DB, in io.Reader, out, errw io.Writer, prompt bool) error {
	sc := bufio.NewScanner(in)
	for {
		if prompt {
			fmt.Fprint(out, "forgedb> ")
		}
		if !sc.Scan() {
			break
		}
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if line == ".quit" {
			return nil
		}
		res, err := db.Exec(line)
		if err != nil {
			fmt.Fprintln(errw, err)
			continue
		}
		writeResult(out, res)
	}
	return sc.Err()
}

func writeResult(out io.Writer, res *forgedb.Result) {
	if res.Columns != nil {
		fmt.Fprintln(out, strings.Join(res.Columns, "\t"))
		for _, row := range res.Rows {
			cells := make([]string, len(row))
			for i, val := range row {
				cells[i] = formatCell(val)
			}
			fmt.Fprintln(out, strings.Join(cells, "\t"))
		}
		fmt.Fprintf(out, "(%d rows)\n", len(res.Rows))
		return
	}
	fmt.Fprintf(out, "changed %d\n", res.Changed)
}

func formatCell(v forgedb.Value) string {
	if v.Null {
		return "NULL"
	}
	if v.Text != nil {
		s := string(v.Text)
		if strings.ContainsAny(s, "\t\n '") {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
		return s
	}
	return strconv.FormatInt(v.Int, 10)
}
