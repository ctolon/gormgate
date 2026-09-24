// Command legacy creates the database this example adopts, by running
// schema.sql. It stands in for whatever built your database before you
// heard of gormgate.
package main

import (
	_ "embed"
	"fmt"
	"os"

	"example.com/adoptdb/internal/store"
)

//go:embed schema.sql
var schema string

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "legacy:", err)
		os.Exit(1)
	}
}

func run() error {
	db, err := store.Open()
	if err != nil {
		return err
	}
	if err := db.Exec(schema).Error; err != nil {
		return err
	}
	fmt.Printf("created %s from schema.sql\n", store.Path())
	return nil
}
