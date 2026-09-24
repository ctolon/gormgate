// Package blog holds the example project's blog models.
package blog

import (
	"time"

	"example.com/blogproj/auth"
)

// Post is a blog post.
type Post struct {
	ID        uint      `gorm:"primaryKey"`
	Title     string    `gorm:"size:200;not null"`
	Body      string    `gorm:"type:text"`
	AuthorID  uint      `gorm:"not null"`
	Author    auth.User `gorm:"constraint:OnDelete:CASCADE"`
	CreatedAt time.Time
}

func (Post) TableName() string { return "blog_posts" }

// Comment is a comment on a post.
type Comment struct {
	ID     uint `gorm:"primaryKey"`
	PostID uint `gorm:"index"`
	Post   Post
	Body   string `gorm:"size:500"`
}

func (Comment) TableName() string { return "blog_comments" }
