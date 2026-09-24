// Package auth holds the example project's user models.
package auth

import "time"

// User is a blog author.
type User struct {
	ID        uint   `gorm:"primaryKey"`
	Name      string `gorm:"size:100;not null;index"`
	Email     string `gorm:"size:190;unique"`
	CreatedAt time.Time
}

func (User) TableName() string { return "auth_users" }
