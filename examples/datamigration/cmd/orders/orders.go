package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"gorm.io/gorm"

	"example.com/datamigration/shop"
)

// seed inserts rows as maps rather than as shop.Order values: shop.Order
// has a Currency field, and at this point in the history the table has no
// currency column yet.
func seed(db *gorm.DB) error {
	rows := []map[string]any{
		{"item": "kettle", "amount_cents": 2999, "country": "DE"},
		{"item": "teapot", "amount_cents": 1850, "country": "GB"},
		{"item": "mug", "amount_cents": 900, "country": "US"},
		{"item": "cafetiere", "amount_cents": 3400, "country": "FR"},
	}
	if err := db.Table("shop_orders").Create(rows).Error; err != nil {
		return err
	}
	fmt.Printf("inserted %d orders\n", len(rows))
	return nil
}

// list prints every order through the current model, which only works once
// both migrations have run.
func list(db *gorm.DB) error {
	var orders []shop.Order
	if err := db.Order("id").Find(&orders).Error; err != nil {
		return err
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ID\tITEM\tAMOUNT\tCOUNTRY\tCURRENCY")
	for _, o := range orders {
		fmt.Fprintf(w, "%d\t%s\t%d\t%s\t%s\n", o.ID, o.Item, o.AmountCents, o.Country, o.Currency)
	}
	return w.Flush()
}
