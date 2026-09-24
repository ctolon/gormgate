package autodetector

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/ctolon/gormgate/internal/fromgorm"
	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/questioner"
	"github.com/ctolon/gormgate/internal/writer"
)

type User struct {
	ID        uint
	Name      string `gorm:"size:100;not null;index"`
	CreatedAt time.Time
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Posts     []Post
	Languages []Language `gorm:"many2many:user_languages;"`
}

type Post struct {
	ID     uint
	Title  string `gorm:"size:200;default:'untitled'"`
	UserID uint
}

type Language struct {
	Code string `gorm:"primaryKey;size:5"`
}

func TestInitialSmoke(t *testing.T) {
	to, err := fromgorm.ProjectState([]fromgorm.App{{Label: "blog", Models: []any{&User{}, &Post{}, &Language{}}}}, fromgorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	from, _ := graph.New().MakeState(nil, true, nil, nil)
	ch, err := New(from, to, &questioner.Base{SpecifiedApps: map[string]bool{"blog": true}}).ChangesFor(graph.New(), ChangesOptions{TrimToApps: []string{"blog"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, mig := range ch["blog"] {
		for _, op := range mig.Operations {
			fmt.Println(op.Describe())
		}
		w := &writer.Writer{Migration: mig, IncludeHeader: true}
		out, err := w.AsString()
		if err != nil {
			t.Fatal(err)
		}
		fmt.Println(string(out))
	}
}
