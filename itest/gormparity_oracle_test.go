//go:build integration

package itest

import (
	"database/sql"
	"time"

	"gorm.io/gorm"
)

// The Oracle gorm-parity corpus.
//
// TestGormParity compares the schema gormgate builds with the schema gorm's
// AutoMigrate builds, so every model of the corpus has to be one that
// gorm-oracle v1.1.3 can actually migrate. Three tags of the shared corpus
// in gormparity_test.go are therefore left out here; each of them is a
// gorm-oracle limitation, verified against gvenzl/oracle-free 23:
//
//   - `type:text`: Dialector.DataTypeOf passes an unknown gorm data type
//     through upper-cased, so the column is created as TEXT, which Oracle
//     does not have:
//     CREATE TABLE "gp_authors" (... "bio" TEXT ...) -> ORA-00902: invalid
//     datatype.
//   - `check:`: Migrator.CreateTable quotes the column names inside the
//     check expression once per matching field, so "age" >= 0 ends up as
//     CHECK ("""""age""""" >= """"0"""") -> ORA-01741: illegal zero-length
//     identifier.
//   - `comment:`: gorm-oracle emits no COMMENT ON statement at all, so a
//     commented column can never match between the two schemas. gormgate
//     does create comments on Oracle (Django's supports_comments is True
//     and the conformance cases table_comment, add_field_comment and
//     alter_field_comment run against Oracle).
//
// `not null` on a string column is left out for a different reason: Oracle
// stores '' as NULL, so Django's interprets_empty_strings_as_nulls makes
// every string column nullable, and gormgate follows Django there while
// gorm-oracle emits NOT NULL. `not null` on non-string columns is kept.

type goAuthor struct {
	ID        uint      `gorm:"primaryKey"`
	Name      string    `gorm:"size:120;uniqueIndex:idx_go_author_name_email,priority:1"`
	Email     string    `gorm:"size:190;uniqueIndex:idx_go_author_name_email,priority:2"`
	Nick      *string   `gorm:"size:40;unique"`
	Age       int16     `gorm:"default:30"`
	Score     float64   `gorm:"precision:10;scale:3"`
	Active    bool      `gorm:"default:true"`
	Bio       string    `gorm:"size:2000"`
	Joined    time.Time `gorm:"precision:3"`
	Avatar    []byte
	Rank      uint32 `gorm:"index:idx_go_author_rank,sort:desc"`
	CreatedAt time.Time
	UpdatedAt int64          `gorm:"autoUpdateTime"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Address   goAddress      `gorm:"embedded;embeddedPrefix:addr_"`
	Profile   goProfile      `gorm:"foreignKey:AuthorID"`
	Posts     []goPost       `gorm:"foreignKey:AuthorID;constraint:OnDelete:CASCADE"`
	Tags      []goTag        `gorm:"many2many:go_author_tags"`
	Manager   *goAuthor
	ManagerID *uint
	Nullable  sql.NullString `gorm:"size:10"`
}

func (goAuthor) TableName() string { return "go_authors" }

type goAddress struct {
	Street string `gorm:"size:100"`
	City   string `gorm:"size:50;index"`
}

type goProfile struct {
	ID       uint
	AuthorID uint   `gorm:"uniqueIndex"`
	Website  string `gorm:"size:255"`
}

func (goProfile) TableName() string { return "go_profiles" }

type goPost struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	AuthorID uint
	Title    string      `gorm:"size:200;default:'untitled'"`
	Views    int         `gorm:"not null;default:0"`
	Comments []goComment `gorm:"polymorphic:Owner"`
}

func (goPost) TableName() string { return "go_posts" }

type goComment struct {
	ID        uint
	OwnerID   uint
	OwnerType string `gorm:"size:50"`
	Body      string `gorm:"size:500"`
}

func (goComment) TableName() string { return "go_comments" }

type goTag struct {
	Code  string `gorm:"primaryKey;size:20"`
	Label string `gorm:"size:50"`
}

func (goTag) TableName() string { return "go_tags" }

type goPair struct {
	Left  int64  `gorm:"primaryKey;autoIncrement:false"`
	Right string `gorm:"primaryKey;size:30"`
	Note  string `gorm:"size:40"`
}

func (goPair) TableName() string { return "go_pairs" }

// goPairRef is gpPairRef for Oracle: a composite foreign key over both
// columns of goPair's composite primary key.
type goPairRef struct {
	ID        uint `gorm:"primaryKey"`
	PairLeft  int64
	PairRight string `gorm:"size:30"`
	Pair      goPair `gorm:"foreignKey:PairLeft,PairRight;references:Left,Right"`
}

func (goPairRef) TableName() string { return "go_pair_refs" }

// oracleParityModels is the corpus TestGormParity uses for the oracle key.
func oracleParityModels() []any {
	return []any{&goAuthor{}, &goProfile{}, &goPost{}, &goComment{}, &goTag{}, &goPair{}, &goPairRef{}}
}
