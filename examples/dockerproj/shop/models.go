// Package shop holds the example's models.
package shop

import "time"

// Order is one placed order.
type Order struct {
	ID          uint      `gorm:"primaryKey"`
	Item        string    `gorm:"size:100;not null"`
	AmountCents int       `gorm:"not null"`
	PlacedAt    time.Time `gorm:"autoCreateTime"`
}

func (Order) TableName() string { return "shop_orders" }
