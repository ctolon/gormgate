// Package shop holds the models of the app that lives on the "default"
// database.
package shop

// Order is one placed order.
type Order struct {
	ID          uint   `gorm:"primaryKey"`
	Item        string `gorm:"size:100;not null"`
	AmountCents int    `gorm:"not null"`
}

func (Order) TableName() string { return "shop_orders" }
