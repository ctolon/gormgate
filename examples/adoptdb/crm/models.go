// Package crm holds the models of the adopted database. They started as
// `inspectdb` output: the models were renamed, the `Managed: false` meta
// was dropped so that gormgate takes ownership of the tables, the index
// and the relation inspectdb reported in a comment were declared, and the
// fields were given Go names. Renaming a field is safe; renaming a table
// or a column is not, because those have to keep matching the database.
package crm

import "time"

// Customer is crm_customers.
type Customer struct {
	ID       *int32 `gorm:"primaryKey;autoIncrement"`
	Name     string `gorm:"type:TEXT;not null"`
	Email    string `gorm:"type:TEXT;unique;not null"`
	SignedUp *time.Time
}

func (Customer) TableName() string { return "crm_customers" }

// Contract is crm_contracts.
type Contract struct {
	ID         *int32   `gorm:"primaryKey;autoIncrement"`
	CustomerID int32    `gorm:"not null;index"`
	Customer   Customer `gorm:"references:ID"`
	ValueCents int32    `gorm:"not null"`
}

func (Contract) TableName() string { return "crm_contracts" }
