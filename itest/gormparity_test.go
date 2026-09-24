//go:build integration

package itest

import (
	"context"
	"database/sql"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/ctolon/gormgate/internal/fromgorm"
	"github.com/ctolon/gormgate/itest/internal/conformance"
	"github.com/ctolon/gormgate/itest/internal/dbtest"
	"github.com/ctolon/gormgate/itest/internal/harness"
)

// The gorm-parity corpus: models exercising gorm's tag vocabulary.

type gpAuthor struct {
	ID        uint      `gorm:"primaryKey"`
	Name      string    `gorm:"size:120;not null;uniqueIndex:idx_gp_author_name_email,priority:1"`
	Email     string    `gorm:"size:190;uniqueIndex:idx_gp_author_name_email,priority:2"`
	Nick      *string   `gorm:"size:40;unique"`
	Age       int16     `gorm:"default:30;check:chk_gp_author_age,age >= 0"`
	Score     float64   `gorm:"precision:10;scale:3"`
	Active    bool      `gorm:"default:true"`
	Bio       string    `gorm:"type:text;comment:author biography"`
	Joined    time.Time `gorm:"precision:3"`
	Avatar    []byte
	Rank      uint32 `gorm:"index:idx_gp_author_rank,sort:desc"`
	CreatedAt time.Time
	UpdatedAt int64          `gorm:"autoUpdateTime"`
	DeletedAt gorm.DeletedAt `gorm:"index"`
	Address   gpAddress      `gorm:"embedded;embeddedPrefix:addr_"`
	Profile   gpProfile      `gorm:"foreignKey:AuthorID"`
	Posts     []gpPost       `gorm:"foreignKey:AuthorID;constraint:OnDelete:CASCADE"`
	Tags      []gpTag        `gorm:"many2many:gp_author_tags"`
	Manager   *gpAuthor
	ManagerID *uint
	Nullable  sql.NullString `gorm:"size:10"`
}

func (gpAuthor) TableName() string { return "gp_authors" }

type gpAddress struct {
	Street string `gorm:"size:100"`
	City   string `gorm:"size:50;index"`
}

type gpProfile struct {
	ID       uint
	AuthorID uint   `gorm:"uniqueIndex"`
	Website  string `gorm:"size:255"`
}

func (gpProfile) TableName() string { return "gp_profiles" }

type gpPost struct {
	ID       uint64 `gorm:"primaryKey;autoIncrement"`
	AuthorID uint
	Title    string      `gorm:"size:200;not null;default:'untitled'"`
	Views    int         `gorm:"not null;default:0"`
	Comments []gpComment `gorm:"polymorphic:Owner"`
}

func (gpPost) TableName() string { return "gp_posts" }

type gpComment struct {
	ID        uint
	OwnerID   uint
	OwnerType string `gorm:"size:50"`
	Body      string `gorm:"size:500"`
}

func (gpComment) TableName() string { return "gp_comments" }

type gpTag struct {
	Code  string `gorm:"primaryKey;size:20"`
	Label string `gorm:"size:50"`
}

func (gpTag) TableName() string { return "gp_tags" }

type gpPair struct {
	Left  int64  `gorm:"primaryKey;autoIncrement:false"`
	Right string `gorm:"primaryKey;size:30"`
	Note  string `gorm:"size:40"`
}

func (gpPair) TableName() string { return "gp_pairs" }

// gpPairRef exercises a composite foreign key: both columns of gpPair's
// composite primary key are referenced by one table-level constraint, which
// gorm names fk_gp_pair_refs_pair.
type gpPairRef struct {
	ID        uint `gorm:"primaryKey"`
	PairLeft  int64
	PairRight string `gorm:"size:30"`
	Pair      gpPair `gorm:"foreignKey:PairLeft,PairRight;references:Left,Right"`
}

func (gpPairRef) TableName() string { return "gp_pair_refs" }

func gormParityModels() []any {
	return []any{&gpAuthor{}, &gpProfile{}, &gpPost{}, &gpComment{}, &gpTag{}, &gpPair{}, &gpPairRef{}}
}

// ddlRecorder is a gorm logger recording DDL statements.
type ddlRecorder struct {
	logger.Interface
	mu  sync.Mutex
	ddl []string
}

var ddlPattern = regexp.MustCompile(`(?is)^\s*(CREATE|ALTER|DROP|COMMENT|RENAME|EXEC\s+sp_rename)\b`)

func (r *ddlRecorder) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, _ := fc()
	if ddlPattern.MatchString(sql) {
		r.mu.Lock()
		r.ddl = append(r.ddl, sql)
		r.mu.Unlock()
	}
}

func TestGormParity(t *testing.T) {
	for _, key := range vendorsUnderTest(t) {
		key := key
		t.Run(key, func(t *testing.T) {
			if key == "clickhouse" {
				t.Skip("ClickHouse has no foreign keys or unique constraints; its subset is covered by TestClickHouse")
			}
			gdb, _ := dbtest.Open(t, key)
			ours, _ := dbtest.Open(t, key)
			models := gormParityModels()
			if key == "oracle" {
				// gorm-oracle cannot AutoMigrate three tags of the shared
				// corpus; see gormparity_oracle_test.go.
				models = oracleParityModels()
			}
			if err := gdb.AutoMigrate(models...); err != nil {
				t.Fatalf("gorm AutoMigrate: %v", err)
			}
			p := harness.New(t, ours, "gp")
			migs := p.MakeMigrations(nil, fromgorm.App{Label: "gp", Models: models})
			if len(migs) == 0 {
				t.Fatal("no migrations generated")
			}
			p.MustMigrate()

			st, err := fromgorm.ProjectState([]fromgorm.App{{Label: "gp", Models: models}}, fromgorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			var tables []string
			for _, ms := range st.Models {
				tables = append(tables, ms.Table)
			}
			slices.Sort(tables)
			pg := harness.New(t, gdb, "gp")
			norm := conformance.Normalizers[key]
			want, err := conformance.Take(pg.Conn.Introspection(), tables, norm)
			if err != nil {
				t.Fatal(err)
			}
			got, err := conformance.Take(p.Conn.Introspection(), tables, norm)
			if err != nil {
				t.Fatal(err)
			}
			if len(want) != len(tables) {
				t.Fatalf("gorm created %d of %d tables", len(want), len(tables))
			}
			if d := conformance.Diff(want, got); d != "" {
				t.Errorf("schema created by gormgate differs from gorm AutoMigrate:\n%s", d)
			}

			// AutoMigrate on the gormgate-created schema must want exactly
			// what it wants on its own schema: gorm is not idempotent for
			// every type (it re-alters some columns on each run), so its
			// second run over its own tables is the reference.
			// gorm's own second run is not always successful either (on TiDB
			// it cannot see its own CHECK constraints and adds them twice),
			// so the reference includes how that run ends.
			baseline := &ddlRecorder{Interface: logger.Discard}
			baseErr := gdb.Session(&gorm.Session{Logger: baseline}).AutoMigrate(models...)
			rec := &ddlRecorder{Interface: logger.Discard}
			ourErr := ours.Session(&gorm.Session{Logger: rec}).AutoMigrate(models...)
			if errText(ourErr) != errText(baseErr) {
				t.Fatalf("AutoMigrate over the gormgate schema ended with %v, over gorm's own schema with %v", ourErr, baseErr)
			}
			if baseErr != nil {
				t.Logf("gorm cannot re-run AutoMigrate over its own schema either: %v", baseErr)
			}
			if strings.Join(rec.ddl, "\n") != strings.Join(baseline.ddl, "\n") {
				t.Errorf("gorm AutoMigrate over the gormgate schema:\n%s\nover its own schema:\n%s",
					strings.Join(rec.ddl, "\n"), strings.Join(baseline.ddl, "\n"))
			} else if len(rec.ddl) > 0 {
				t.Logf("gorm re-applies on every run (also on its own schema):\n%s", strings.Join(rec.ddl, "\n"))
			}
		})
	}
}

// errText renders an error for comparison ("" for nil).
func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
