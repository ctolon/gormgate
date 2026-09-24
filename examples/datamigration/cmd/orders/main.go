// Command orders seeds and prints the orders table.
//
//	orders seed   insert a few orders
//	orders list   print every order
//
// Seeding runs between the two migrations, so that 0002_order_currency has
// rows to backfill.
package main

import (
	"fmt"
	"os"

	"example.com/datamigration/internal/store"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: orders seed|list")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "orders:", err)
		os.Exit(1)
	}
}

func run(command string) error {
	db, err := store.Open()
	if err != nil {
		return err
	}
	switch command {
	case "seed":
		return seed(db)
	case "list":
		return list(db)
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}
