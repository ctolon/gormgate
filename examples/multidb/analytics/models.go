// Package analytics holds the models of the app that lives on the
// "reports" database.
package analytics

import "time"

// PageView is one recorded page view.
type PageView struct {
	ID   uint   `gorm:"primaryKey"`
	Path string `gorm:"size:200;not null;index"`
	Seen time.Time
}

func (PageView) TableName() string { return "analytics_page_views" }
