// Hand-written: `makemigrations --empty shop` produced the file and the
// three operations below were written into it.

package migrations

import (
	"fmt"

	m "github.com/ctolon/gormgate/migrations"
)

func init() {
	m.Register(&m.Migration{
		App:          "shop",
		Name:         "0002_order_currency",
		Dependencies: []m.Key{m.Dep("shop", "0001_initial")},
		Operations: []m.Operation{
			// 1. Add the column nullable, so that the rows already in the
			//    table are accepted.
			&m.AddField{
				ModelName: "order",
				Name:      "currency",
				Field:     m.Field{Type: m.String, Size: 3, Null: true},
			},
			// 2. Fill it.
			&m.RunGo{Code: backfillCurrency, ReverseCode: m.RunGoNoop},
			// 3. Tighten it to NOT NULL, which is what the model declares.
			&m.AlterField{
				ModelName: "order",
				Name:      "currency",
				Field:     m.Field{Type: m.String, Size: 3},
			},
		},
	})
}

// currencyFor maps a country to the currency its orders are billed in. A
// real project would read this from a currencies table; keeping it in the
// migration means there is only one file to read.
var currencyFor = map[string]string{
	"DE": "EUR",
	"FR": "EUR",
	"GB": "GBP",
	"US": "USD",
}

// backfillCurrency fills shop_orders.currency from shop_orders.country.
//
// It must be a package-level function, not a closure: a closure has no name
// to write into a generated file, and gormgate refuses one.
func backfillCurrency(apps *m.Apps, ed m.SchemaEditor) error {
	// The historical model: the columns shop_orders has at this point in
	// the history, not the ones shop.Order declares today.
	order, err := apps.GetModel("shop", "order")
	if err != nil {
		return err
	}
	rows, err := order.Objects(ed).Find(nil)
	if err != nil {
		return err
	}
	for _, row := range rows {
		country, ok := row["country"].(string)
		if !ok {
			return fmt.Errorf("order %v: country is %T, want string", row["id"], row["country"])
		}
		currency, ok := currencyFor[country]
		if !ok {
			return fmt.Errorf("order %v: no currency known for country %q", row["id"], country)
		}
		if _, err := order.Objects(ed).Update(
			map[string]any{"id": row["id"]},
			map[string]any{"currency": currency},
		); err != nil {
			return err
		}
	}
	return nil
}
