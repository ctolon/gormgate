// Package shop holds the example's models.
package shop

// Order is one placed order. Country is where the order was placed from;
// Currency is what it is billed in, derived from Country by migration
// 0002_order_currency.
type Order struct {
	ID          uint   `gorm:"primaryKey"`
	Item        string `gorm:"size:100;not null"`
	AmountCents int    `gorm:"not null"`
	Country     string `gorm:"size:2;not null"`
	Currency    string `gorm:"size:3;not null"`
}

func (Order) TableName() string { return "shop_orders" }
