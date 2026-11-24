package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"forgedb"
)

func main() {
	dir, err := os.MkdirTemp("", "forgedb")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "forge.db")

	db, err := forgedb.Create(path)
	if err != nil {
		log.Fatal(err)
	}
	page, err := db.Allocate()
	if err != nil {
		log.Fatal(err)
	}
	copy(page.Payload(), []byte("Hello ForgeDB"))
	if err := db.Write(page); err != nil {
		log.Fatal(err)
	}
	if err := db.Close(); err != nil {
		log.Fatal(err)
	}

	db, err = forgedb.Open(path)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	page, err = db.Read(page.ID())
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(page.Payload()[:13]))
}
