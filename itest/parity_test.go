//go:build parity && integration

// The parity harness runs the same command sequences against a real
// Django 6.0 project (Python 3.14 in Docker) and an equivalent gormgate
// project, and requires the two to agree on what they did rather than on
// how they said it: the same exit status, the same migrations written, and
// the same plan of migrations and operations to run.
package itest

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

const parityImage = "gormgate-parity:django6"

// step is one command run against both projects, after installing the
// given model sources.
type step struct {
	// Models maps app label to the model source of each side.
	Django map[string]string
	Go     map[string]string
	// DjangoFiles and GoFiles are extra files written before the command
	// (paths relative to the project root).
	DjangoFiles map[string]string
	GoFiles     map[string]string
	// DeleteMigrations names migrations to remove from both projects
	// before the command runs, as "<app>/<name>"; the suffix is ".py" on
	// Django's side and ".go" on gormgate's.
	DeleteMigrations []string
	// Argv is gormgate's command line. djangoArgv translates the options
	// Django spells differently.
	Argv  []string
	Stdin string
	// Env is set on both sides for this command only.
	Env map[string]string
	// WantExit is the expected exit status of both sides.
	WantExit int
}

type scenario struct {
	Name  string
	Steps []step
}

// goModels renders a Go models.go file from the struct sources.
func goModels(pkg string, imports string, structs string, models string) string {
	return "package " + pkg + "\n\n" + imports + "\n" + structs + "\n\nfunc Models() []any { return []any{" + models + "} }\n"
}

var scenarios = []scenario{
	{
		Name: "initial_and_migrate",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": `from django.db import models


class User(models.Model):
    name = models.CharField(max_length=100)
    email = models.CharField(max_length=190, unique=True)
`,
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    body = models.TextField(null=True)
    author = models.ForeignKey("auth_app.User", on_delete=models.CASCADE)
`,
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", `type User struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Name  string `+"`gorm:\"size:100;not null\"`"+`
	Email string `+"`gorm:\"size:190;unique;not null\"`"+`
}`, "&User{}"),
					"blog": goModels("blog", `import "example.com/parityproj/auth_app"`, `type Post struct {
	ID       uint   `+"`gorm:\"primaryKey\"`"+`
	Title    string `+"`gorm:\"size:200;not null\"`"+`
	Body     string `+"`gorm:\"type:text\"`"+`
	AuthorID uint   `+"`gorm:\"not null\"`"+`
	Author   auth_app.User `+"`gorm:\"constraint:OnDelete:CASCADE\"`"+`
}`, "&Post{}"),
				},
				Argv: []string{"makemigrations", "auth_app", "blog"},
			},
			{Argv: []string{"makemigrations"}},
			{Argv: []string{"migrate"}},
			{Argv: []string{"migrate"}},
			{Argv: []string{"showmigrations"}},
			{Argv: []string{"showmigrations", "--plan"}},
			{Argv: []string{"migrate", "--check"}},
			{Argv: []string{"makemigrations", "--check"}},
			{Argv: []string{"migrate", "blog", "zero"}},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate", "blog"}},
			{Argv: []string{"migrate", "--plan"}},
		},
	},
	{
		Name: "add_field_questioner",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": `from django.db import models
`,
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
`,
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", "", ""),
					"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
}`, "&Post{}"),
				},
				Argv: []string{"makemigrations", "blog"},
			},
			{Argv: []string{"migrate"}},
			{
				// A nullable field needs no prompt.
				Django: map[string]string{"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    subtitle = models.CharField(max_length=200, null=True)
`},
				Go: map[string]string{"blog": goModels("blog", "", `type Post struct {
	ID       uint   `+"`gorm:\"primaryKey\"`"+`
	Title    string `+"`gorm:\"size:200;not null\"`"+`
	Subtitle string `+"`gorm:\"size:200\"`"+`
}`, "&Post{}")},
				Argv: []string{"makemigrations", "blog"},
			},
			{
				// A non-nullable field without a default: --no-input exits 3.
				Django: map[string]string{"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    subtitle = models.CharField(max_length=200, null=True)
    views = models.IntegerField()
`},
				Go: map[string]string{"blog": goModels("blog", "", `type Post struct {
	ID       uint   `+"`gorm:\"primaryKey\"`"+`
	Title    string `+"`gorm:\"size:200;not null\"`"+`
	Subtitle string `+"`gorm:\"size:200\"`"+`
	Views    int    `+"`gorm:\"not null\"`"+`
}`, "&Post{}")},
				Argv:     []string{"makemigrations", "blog", "--no-input"},
				WantExit: 3,
			},
			{
				// Interactively provide a one-off default.
				Argv:  []string{"makemigrations", "blog"},
				Stdin: "1\n0\n",
			},
			{Argv: []string{"migrate"}},
			{Argv: []string{"showmigrations", "blog"}},
		},
	},
	{
		Name: "renames_and_options",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": "from django.db import models\n",
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    body = models.TextField(null=True)
`,
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", "", ""),
					"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
	Body  string `+"`gorm:\"type:text\"`"+`
}`, "&Post{}"),
				},
				Argv: []string{"makemigrations", "blog", "--name", "create_post"},
			},
			{
				// Renaming a field: the questioner asks, the answer is no.
				Django: map[string]string{"blog": `from django.db import models


class Post(models.Model):
    headline = models.CharField(max_length=200)
    body = models.TextField(null=True)
`},
				Go: map[string]string{"blog": goModels("blog", "", `type Post struct {
	ID       uint   `+"`gorm:\"primaryKey\"`"+`
	Headline string `+"`gorm:\"size:200;not null\"`"+`
	Body     string `+"`gorm:\"type:text\"`"+`
}`, "&Post{}")},
				Argv:  []string{"makemigrations", "blog", "--dry-run"},
				Stdin: "N\n",
			},
			{
				// The same change, answered yes, with a chosen name.
				Argv:  []string{"makemigrations", "blog", "-n", "rename_title"},
				Stdin: "y\n",
			},
			{
				// Renaming the model too.
				Django: map[string]string{"blog": `from django.db import models


class Article(models.Model):
    headline = models.CharField(max_length=200)
    body = models.TextField(null=True)

    class Meta:
        db_table = "blog_post"
`},
				Go: map[string]string{"blog": goModels("blog", "", `type Article struct {
	ID       uint   `+"`gorm:\"primaryKey\"`"+`
	Headline string `+"`gorm:\"size:200;not null\"`"+`
	Body     string `+"`gorm:\"type:text\"`"+`
}

func (Article) TableName() string { return "blog_post" }`, "&Article{}")},
				Argv:  []string{"makemigrations", "blog"},
				Stdin: "y\n",
			},
			{Argv: []string{"migrate", "-v"}},
			{Argv: []string{"showmigrations", "-v"}},
			{Argv: []string{"makemigrations", "blog", "--empty", "--name", "manual"}},
			{Argv: []string{"makemigrations", "blog", "--empty", "--dry-run", "-vv", "--name", "shown"}},
		},
	},
	{
		Name: "update",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": "from django.db import models\n",
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
`,
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", "", ""),
					"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
}`, "&Post{}"),
				},
				Argv: []string{"makemigrations", "blog"},
			},
			{
				// --update merges the change into the last migration and
				// deletes the previous file.
				Django: map[string]string{"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    slug = models.CharField(max_length=50, null=True)
`},
				Go: map[string]string{"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
	Slug  string `+"`gorm:\"size:50\"`"+`
}`, "&Post{}")},
				Argv: []string{"makemigrations", "blog", "--update"},
			},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate"}},
		},
	},
	{
		Name: "merge",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": "from django.db import models\n",
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
`,
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", "", ""),
					"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
}`, "&Post{}"),
				},
				Argv: []string{"makemigrations", "blog"},
			},
			{
				// Two migrations with the same parent: a conflict.
				DjangoFiles: map[string]string{
					"blog/migrations/0002_a.py": `from django.db import migrations


class Migration(migrations.Migration):
    dependencies = [("blog", "0001_initial")]
    operations = [migrations.RunSQL("SELECT 1", migrations.RunSQL.noop)]
`,
					"blog/migrations/0002_b.py": `from django.db import migrations


class Migration(migrations.Migration):
    dependencies = [("blog", "0001_initial")]
    operations = [migrations.RunSQL("SELECT 2", migrations.RunSQL.noop)]
`,
				},
				GoFiles: map[string]string{
					"blog/migrations/0002_a.go": `package migrations

import m "github.com/ctolon/gormgate/migrations"

func init() {
	m.Register(&m.Migration{
		App:          "blog",
		Name:         "0002_a",
		Dependencies: []m.Key{{App: "blog", Name: "0001_initial"}},
		Operations:   []m.Operation{&m.RunSQL{SQL: m.Script("SELECT 1"), ReverseSQL: m.NoSQL}},
	})
}
`,
					"blog/migrations/0002_b.go": `package migrations

import m "github.com/ctolon/gormgate/migrations"

func init() {
	m.Register(&m.Migration{
		App:          "blog",
		Name:         "0002_b",
		Dependencies: []m.Key{{App: "blog", Name: "0001_initial"}},
		Operations:   []m.Operation{&m.RunSQL{SQL: m.Script("SELECT 2"), ReverseSQL: m.NoSQL}},
	})
}
`,
				},
				Argv:     []string{"makemigrations", "blog"},
				WantExit: 1,
			},
			{Argv: []string{"makemigrations", "blog", "--merge", "--no-input"}},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate"}},
		},
	},
	{
		Name: "errors",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": "from django.db import models\n",
					"blog":     "from django.db import models\n",
				},
				Go: map[string]string{
					"auth_app": goModels("auth_app", "", "", ""),
					"blog":     goModels("blog", "", "", ""),
				},
				Argv:     []string{"makemigrations", "nosuchapp"},
				WantExit: 2,
			},
			{Argv: []string{"migrate", "nosuchapp"}, WantExit: 1},
			{Argv: []string{"migrate", "blog"}},
			{Argv: []string{"sqlmigrate", "blog", "0001"}, WantExit: 1},
			{Argv: []string{"squashmigrations", "blog", "0001"}, WantExit: 1},
			{Argv: []string{"optimizemigration", "blog", "0001"}, WantExit: 1},
			{Argv: []string{"makemigrations", "--empty"}, WantExit: 1},
			{Argv: []string{"migrate", "--prune"}, WantExit: 1},
			{Argv: []string{"showmigrations", "nosuchapp"}, WantExit: 2},
		},
	},
	{
		// An app whose migrations are disabled (MIGRATION_MODULES ->
		// None) is created by `migrate --run-syncdb` instead, and is
		// reported as unmigrated everywhere else.
		Name: "run_syncdb_unmigrated_app",
		Steps: []step{
			{
				Django: map[string]string{
					"auth_app": `from django.db import models


class User(models.Model):
    name = models.CharField(max_length=100)


class Audit(models.Model):
    note = models.CharField(max_length=100)
`,
					"blog": `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
`,
				},
				Go: map[string]string{
					// User before Audit, which is not alphabetical: the
					// tables must be created in the declared order, as
					// Django's sync_apps does.
					"auth_app": goModels("auth_app", "", `type User struct {
	ID   uint   `+"`gorm:\"primaryKey\"`"+`
	Name string `+"`gorm:\"size:100;not null\"`"+`
}

func (User) TableName() string { return "auth_app_user" }

type Audit struct {
	ID   uint   `+"`gorm:\"primaryKey\"`"+`
	Note string `+"`gorm:\"size:100;not null\"`"+`
}

func (Audit) TableName() string { return "auth_app_audit" }`, "&User{}, &Audit{}"),
					"blog": goModels("blog", "", `type Post struct {
	ID    uint   `+"`gorm:\"primaryKey\"`"+`
	Title string `+"`gorm:\"size:200;not null\"`"+`
}

func (Post) TableName() string { return "blog_post" }`, "&Post{}"),
				},
				Argv: []string{"makemigrations"},
			},
			{Argv: []string{"showmigrations"}, Env: disableAuthApp},
			{Argv: []string{"migrate"}, Env: disableAuthApp},
			{Argv: []string{"migrate", "--run-syncdb"}, Env: disableAuthApp},
			{Argv: []string{"migrate", "--run-syncdb"}, Env: disableAuthApp},
			{Argv: []string{"migrate", "auth_app"}, Env: disableAuthApp, WantExit: 1},
			{Argv: []string{"showmigrations", "auth_app"}, Env: disableAuthApp},
		},
	},
	{
		// sqlmigrate's statements follow gorm's DDL against PostgreSQL on
		// one side and Django's against SQLite on the other, so each step
		// compares the framing only (sqlFraming).
		Name: "sqlmigrate",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{Argv: []string{"sqlmigrate", "blog", "0001"}},
			{Argv: []string{"sqlmigrate", "blog", "0001", "--backwards"}},
			{
				// BEGIN; and COMMIT; are styled with SQL_KEYWORD by
				// BaseCommand.execute, unconditionally.
				Argv: []string{"sqlmigrate", "blog", "0001", "--force-color"},
				Env:  colorsFromPalette,
			},
			{Argv: []string{"makemigrations", "blog", "--empty", "--name", "noop"}},
			{
				// An empty migration produces no statements: "No
				// operations found." goes to stderr and nothing is
				// wrapped in a transaction.
				Argv: []string{"sqlmigrate", "blog", "0002"},
			},
			{Argv: []string{"sqlmigrate", "blog", "0002", "-q"}},
			{Argv: []string{"sqlmigrate", "blog", "9999"}, WantExit: 1},
		},
	},
	{
		Name: "squashmigrations",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{
				Django: map[string]string{"blog": djangoPostSlug},
				Go:     map[string]string{"blog": postSlugModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{
				Django: map[string]string{"blog": djangoPostSlugBody},
				Go:     map[string]string{"blog": postSlugBodyModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{Argv: []string{"migrate"}},
			// The prompt loop: an empty answer means no, and so does "n".
			{Argv: []string{"squashmigrations", "blog", "0003"}, Stdin: "\n"},
			{Argv: []string{"squashmigrations", "blog", "0003"}, Stdin: "maybe\nn\n"},
			// The start-migration form keeps the start's number prefix.
			{Argv: []string{"squashmigrations", "blog", "0002", "0003", "--squashed-name", "tail", "--no-input"}},
			// Squashing from the beginning, without optimizing.
			{Argv: []string{"squashmigrations", "blog", "0001", "--no-optimize", "--squashed-name", "manual", "--no-input"}},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate"}},
			// A squash whose file name is taken.
			{
				Argv:     []string{"squashmigrations", "blog", "0002_tail", "--squashed-name", "manual", "--no-input"},
				WantExit: 1,
			},
			{Argv: []string{"squashmigrations", "nosuchapp", "0001", "--no-input"}, WantExit: 1},
		},
	},
	{
		// optimizemigration works on a hand-written migration whose
		// CreateModel and AddField reduce to one CreateModel.
		Name: "optimizemigration",
		Steps: []step{
			{
				Django:      map[string]string{"auth_app": emptyDjangoApp, "blog": emptyDjangoApp},
				Go:          map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": emptyGoModels("blog")},
				DjangoFiles: map[string]string{"blog/migrations/0001_initial.py": djangoReducible},
				GoFiles:     map[string]string{"blog/migrations/0001_initial.go": goReducible},
				Argv:        []string{"optimizemigration", "blog", "0001", "--check"},
				WantExit:    1,
			},
			{Argv: []string{"optimizemigration", "blog", "0001"}},
			// The file has been rewritten, so there is nothing left to do.
			{Argv: []string{"optimizemigration", "blog", "0001", "--check"}},
			{Argv: []string{"optimizemigration", "blog", "0001", "-q"}},
			{Argv: []string{"optimizemigration", "blog", "9999"}, WantExit: 1},
			{Argv: []string{"optimizemigration", "nosuchapp", "0001"}, WantExit: 1},
		},
	},
	{
		Name: "prune",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{
				Django: map[string]string{"blog": djangoPostSlug},
				Go:     map[string]string{"blog": postSlugModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{Argv: []string{"migrate"}},
			// The second migration is applied but no longer on disk.
			{
				DeleteMigrations: []string{"blog/0002_post_slug"},
				Argv:             []string{"migrate", "blog", "--prune"},
			},
			{Argv: []string{"migrate", "blog", "--prune"}},
			{Argv: []string{"migrate", "blog", "--prune", "-q"}},
			{Argv: []string{"showmigrations", "blog"}},
		},
	},
	{
		// The branch --prune refuses to run in: a squashed migration
		// still names a replaced migration that has been deleted.
		Name: "prune_squashed",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"makemigrations", "blog"},
			},
			{Argv: []string{"migrate"}},
			{Argv: []string{"squashmigrations", "blog", "0001", "--squashed-name", "sq", "--no-input"}},
			{
				DeleteMigrations: []string{"blog/0001_initial"},
				Argv:             []string{"migrate", "blog", "--prune"},
			},
		},
	},
	{
		Name: "global_options",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"makemigrations", "blog", "--no-header"},
			},
			// An unknown --database is a usage error on both sides.
			{Argv: []string{"migrate", "--database", "nosuch"}, WantExit: 2},
			// --check exits 1 while something is unapplied.
			{Argv: []string{"migrate", "--check"}, WantExit: 1},
			{Argv: []string{"migrate", "--plan", "--check"}, WantExit: 1},
			{Argv: []string{"migrate", "--plan", "--force-color"}, Env: colorsFromPalette},
			{Argv: []string{"migrate", "--plan", "--no-color"}},
			{Argv: []string{"migrate", "-q"}},
			{Argv: []string{"migrate", "--check"}},
			{Argv: []string{"migrate", "blog", "zero", "--fake"}},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate", "blog", "--fake-initial"}},
			{Argv: []string{"showmigrations", "blog"}},
			{Argv: []string{"migrate", "--skip-checks", "-v"}},
			// --check exits 1 when a model change has no migration.
			{
				Django:   map[string]string{"blog": djangoPostSlug},
				Go:       map[string]string{"blog": postSlugModel},
				Argv:     []string{"makemigrations", "--check", "--dry-run"},
				WantExit: 1,
			},
			{Argv: []string{"makemigrations", "blog", "--scriptable"}},
			{Argv: []string{"migrate", "nosuchapp"}, WantExit: 1},
			{Argv: []string{"sqlsequencereset"}, WantExit: 2},
			{Argv: []string{"sqlsequencereset", "nosuchapp"}, WantExit: 1},
			{
				// Two apps at once, so that two migrations are written in
				// one run.
				Django: map[string]string{"auth_app": djangoUser},
				Go:     map[string]string{"auth_app": userModel},
				Argv:   []string{"makemigrations", "--dry-run", "-vv"},
			},
		},
	},
	{
		// sqlsequencereset prints pure SQL with nothing Python or Go in
		// it, so both sides can be compared statement for statement --
		// but only if they address the same tables. These models
		// therefore carry Django's table names, which is also what makes
		// the two schemas identical on the one PostgreSQL both projects
		// now use.
		Name: "sqlsequencereset",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": djangoSeqUser, "blog": djangoSeqPost},
				Go:     map[string]string{"auth_app": goSeqUser, "blog": goSeqPost},
				Argv:   []string{"makemigrations", "auth_app", "blog"},
			},
			{Argv: []string{"migrate"}},
			// One app, two tables: the statements come out per table, in
			// a defined order.
			{Argv: []string{"sqlsequencereset", "blog"}},
			// Two apps at once, and the reverse order, which must not
			// change the order within an app.
			{Argv: []string{"sqlsequencereset", "blog", "auth_app"}},
			{Argv: []string{"sqlsequencereset", "auth_app", "blog"}},
			// An app whose models have no sequence prints the framing
			// and nothing else.
			{Argv: []string{"sqlsequencereset", "auth_app", "--no-color"}},
		},
	},
	{
		// check runs the system checks on their own. Everything the
		// command frames its report with is Django's byte for byte: the
		// "System check identified no issues (0 silenced)." footer on
		// stdout, the CommandError for an unknown tag, and the
		// LookupError the app registry raises for an app label that is
		// not installed. What cannot be compared is the *content* of the
		// checks -- gormgate's are not Django's -- which is why the only
		// step here that is normalized is --list-tags.
		Name: "check",
		Steps: []step{
			{
				Django: map[string]string{"auth_app": emptyDjangoApp, "blog": djangoPost},
				Go:     map[string]string{"auth_app": emptyGoModels("auth_app"), "blog": postModel},
				Argv:   []string{"check"},
			},
			// --fail-level decides the exit status; neither side has a
			// message to report, so both still pass at WARNING.
			{Argv: []string{"check", "--fail-level", "WARNING"}},
			{Argv: []string{"check", "blog", "auth_app"}},
			// Both sides label their model checks "models", so the same
			// selection runs the same kind of check on both.
			{Argv: []string{"check", "--tag", "models"}},
			{Argv: []string{"check", "-t", "nosuchtag"}, WantExit: 1},
			{
				// Django labels its checks with the ten tags of its own
				// framework (caches, templates, urls, ...); gormgate's
				// checks are its own, and carry the three tags its
				// checks actually have -- "compatibility", "database"
				// and "models", all three of which Django also has.
				//
				// What cannot be added here is a run of
				// "check --tag compatibility": naming that tag is what
				// makes gormgate print its inventory of the project
				// (gormgate.I001), and Django has nothing of the kind
				// to compare it with. Normalizing the body away would
				// leave a step that pins nothing.
				Argv: []string{"check", "--list-tags"},
			},
			{Argv: []string{"check", "nosuchapp"}, WantExit: 1},
			// The whole help of the command, which is where the option
			// names, their order and their wording are pinned.
			{Argv: []string{"help", "check"}},
		},
	},
}

// The model sources the scenarios share.
const (
	emptyDjangoApp = "from django.db import models\n"

	djangoPost = `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
`
	djangoUser = `from django.db import models


class User(models.Model):
    name = models.CharField(max_length=100)
`
	djangoPostSlug = `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    slug = models.CharField(max_length=50, null=True)
`
	djangoPostSlugBody = `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)
    slug = models.CharField(max_length=50, null=True)
    body = models.TextField(null=True)
`
	// djangoReducible and goReducible are the same hand-written migration:
	// a CreateModel the optimizer folds an AddField into.
	djangoReducible = `from django.db import migrations, models


class Migration(migrations.Migration):
    initial = True
    operations = [
        migrations.CreateModel(
            name="Thing",
            fields=[("id", models.BigAutoField(primary_key=True, serialize=False))],
        ),
        migrations.AddField(
            model_name="thing",
            name="name",
            field=models.CharField(max_length=10, null=True),
        ),
    ]
`
	goReducible = `package migrations

import m "github.com/ctolon/gormgate/migrations"

func init() {
	m.Register(&m.Migration{
		App:     "blog",
		Name:    "0001_initial",
		Initial: m.Ptr(true),
		Operations: []m.Operation{
			&m.CreateModel{Name: "Thing", Table: "blog_thing", Fields: m.Fields{
				{Name: "id", Field: m.Field{Type: m.Int, Size: 64, PrimaryKey: true, AutoIncrement: true}},
			}},
			&m.AddField{ModelName: "thing", Name: "name",
				Field: m.Field{Type: m.String, Size: 10, Null: true}},
		},
	})
}
`
)

var (
	// The sqlsequencereset scenario needs both sides to name their tables
	// the same way: Django derives blog_post from the app label and the
	// model, gorm would pluralise it to blog_posts, so the Go models say
	// so explicitly.
	djangoSeqPost = `from django.db import models


class Post(models.Model):
    title = models.CharField(max_length=200)


class Comment(models.Model):
    body = models.CharField(max_length=100)
`
	djangoSeqUser = `from django.db import models


class User(models.Model):
    name = models.CharField(max_length=100)
`
	goSeqPost = goModels("blog", "", "type Post struct {\n\tID    uint   `gorm:\"primaryKey\"`\n\tTitle string `gorm:\"size:200;not null\"`\n}\n\nfunc (Post) TableName() string { return \"blog_post\" }\n\ntype Comment struct {\n\tID   uint   `gorm:\"primaryKey\"`\n\tBody string `gorm:\"size:100;not null\"`\n}\n\nfunc (Comment) TableName() string { return \"blog_comment\" }", "&Post{}, &Comment{}")

	goSeqUser = goModels("auth_app", "", "type User struct {\n\tID   uint   `gorm:\"primaryKey\"`\n\tName string `gorm:\"size:100;not null\"`\n}\n\nfunc (User) TableName() string { return \"auth_app_user\" }", "&User{}")

	postModel = goModels("blog", "", "type Post struct {\n\tID    uint   `gorm:\"primaryKey\"`\n\tTitle string `gorm:\"size:200;not null\"`\n}", "&Post{}")

	userModel = goModels("auth_app", "", "type User struct {\n\tID   uint   `gorm:\"primaryKey\"`\n\tName string `gorm:\"size:100;not null\"`\n}", "&User{}")

	postSlugModel = goModels("blog", "", "type Post struct {\n\tID    uint   `gorm:\"primaryKey\"`\n\tTitle string `gorm:\"size:200;not null\"`\n\tSlug  string `gorm:\"size:50\"`\n}", "&Post{}")

	postSlugBodyModel = goModels("blog", "", "type Post struct {\n\tID    uint   `gorm:\"primaryKey\"`\n\tTitle string `gorm:\"size:200;not null\"`\n\tSlug  string `gorm:\"size:50\"`\n\tBody  string `gorm:\"type:text\"`\n}", "&Post{}")
)

// emptyGoModels is an app whose package declares no models.
func emptyGoModels(pkg string) string { return goModels(pkg, "", "", "") }

// colorsFromPalette clears the GORMGATE_COLORS=nocolor the gormgate side
// otherwise runs with, so that --force-color picks the default palette on
// both sides.
var colorsFromPalette = map[string]string{"GORMGATE_COLORS": ""}

// disableAuthApp switches auth_app's migrations off on both sides.
var disableAuthApp = map[string]string{"PARITY_DISABLE_MIGRATIONS": "auth_app"}

func TestDjangoParity(t *testing.T) {
	buildParityImage(t)
	repo := repoRoot(t)
	for _, sc := range scenarios {
		sc := sc
		t.Run(sc.Name, func(t *testing.T) {
			dj := newDjangoProject(t, repo)
			gg := newParityGoProject(t, repo)
			for i, s := range sc.Steps {
				for app, src := range s.Django {
					dj.writeModels(app, src)
				}
				for app, src := range s.Go {
					gg.writeModels(app, src)
				}
				for path, src := range s.DjangoFiles {
					dj.writeFile(path, src)
				}
				for path, src := range s.GoFiles {
					gg.writeFile(path, src)
				}
				for _, mig := range s.DeleteMigrations {
					dj.deleteMigration(mig)
					gg.deleteMigration(mig)
				}
				djOut, djCode := dj.run(s.Stdin, s.Env, djangoArgv(s.Argv)...)
				ggOut, ggCode := gg.run(s.Stdin, s.Env, s.Argv...)
				if djCode != s.WantExit {
					t.Fatalf("step %d (%v): Django exited %d, the scenario expects %d\n%s", i, s.Argv, djCode, s.WantExit, djOut)
				}
				if ggCode != djCode {
					t.Errorf("step %d (%v): gormgate exited %d, Django %d\ngormgate:\n%s\nDjango:\n%s", i, s.Argv, ggCode, djCode, ggOut, djOut)
				}
				if want, got := dj.migrations(), gg.migrations(); !slices.Equal(want, got) {
					t.Errorf("step %d (%v): the migrations on disk differ\n  Django:   %v\n  gormgate: %v", i, s.Argv, want, got)
				}
				djPlanOut, djPlanCode := dj.run("", nil, "migrate", "--plan")
				ggPlanOut, ggPlanCode := gg.run("", nil, "migrate", "--plan")
				if djPlanCode != ggPlanCode {
					t.Errorf("step %d (%v): migrate --plan exited %d for gormgate, %d for Django\ngormgate:\n%s\nDjango:\n%s",
						i, s.Argv, ggPlanCode, djPlanCode, ggPlanOut, djPlanOut)
					continue
				}
				// A scenario may leave the graph in a state that has no
				// plan at all -- conflicting leaves, before a merge. Both
				// sides refusing it in the same way is the agreement.
				if djPlanCode != 0 {
					continue
				}
				wantPlan, gotPlan := planFacts(djPlanOut), planFacts(ggPlanOut)
				if !slices.Equal(wantPlan, gotPlan) {
					t.Errorf("step %d (%v): the migration plans differ\n  Django:   %v\n  gormgate: %v", i, s.Argv, wantPlan, gotPlan)
				}
			}
		})
	}
}

// parityVersions are the build arguments of the Django image. Each is
// pinned by default and can be overridden from the environment, which is
// how the weekly drift canary points the same suite at the latest releases
// without a second Dockerfile. Keep the defaults in step with the ones in
// parity/Dockerfile.
var parityVersions = []struct{ arg, env, pinned string }{
	{"PYTHON_VERSION", "GORMGATE_PARITY_PYTHON", "3.14.7"},
	{"DJANGO_VERSION", "GORMGATE_PARITY_DJANGO", "6.0.8"},
	{"PSYCOPG_VERSION", "GORMGATE_PARITY_PSYCOPG", "3.3.6"},
}

// buildParityImage builds the Django image once per run, and logs the
// versions it built with so that a failure says what was tested.
func buildParityImage(t *testing.T) {
	t.Helper()
	repo := repoRoot(t)
	args := []string{"build", "-t", parityImage}
	for _, v := range parityVersions {
		value := v.pinned
		if override := os.Getenv(v.env); override != "" {
			value = override
		}
		t.Logf("parity image: %s=%s", v.arg, value)
		args = append(args, "--build-arg", v.arg+"="+value)
	}
	cmd := exec.Command("docker", append(args, filepath.Join(repo, "parity"))...)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	msg := fmt.Sprintf("cannot build the Django parity image (%v):\n%s", err, out)
	// A broken Dockerfile, a failed pip install or a Django release bump
	// must not turn the byte-for-byte guarantee into a green skip when the
	// run asked for the databases to be there.
	if os.Getenv(dbtest.EnvRequire) != "" {
		t.Fatal(msg)
	}
	t.Skip(msg)
}

type djangoProject struct {
	t   *testing.T
	dir string
	dsn string
}

func newDjangoProject(t *testing.T, repo string) *djangoProject {
	t.Helper()
	// The Django project runs on the same PostgreSQL server as the
	// gormgate one, in a throw-away database of its own, so that the two
	// print the same SQL for the same schema. Its settings module builds
	// Django's DATABASES entry from this DSN.
	_, info := dbtest.Open(t, "pg18")
	dir := t.TempDir()
	if err := copyTree(filepath.Join(repo, "parity", "django"), dir); err != nil {
		t.Fatal(err)
	}
	for _, app := range []string{"auth_app", "blog"} {
		if err := os.MkdirAll(filepath.Join(dir, app, "migrations"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, app, "migrations", "__init__.py"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return &djangoProject{t: t, dir: dir, dsn: info.DSN}
}

func (p *djangoProject) writeModels(app, src string) {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, app, "models.py"), []byte(src), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *djangoProject) writeFile(rel, src string) {
	p.t.Helper()
	path := filepath.Join(p.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// deleteMigration removes "<app>/<name>" from the project, which is how a
// scenario makes a recorded migration disappear from disk.
func (p *djangoProject) deleteMigration(mig string) {
	p.t.Helper()
	app, name, _ := strings.Cut(mig, "/")
	if err := os.Remove(filepath.Join(p.dir, app, "migrations", name+".py")); err != nil {
		p.t.Fatal(err)
	}
}

func (p *djangoProject) run(stdin string, env map[string]string, args ...string) (string, int) {
	p.t.Helper()
	// --network host puts the container on the host's network stack, so
	// the DSN dbtest built for the host (127.0.0.1 and a published port)
	// reaches the server unchanged from inside it.
	docker := []string{
		"run", "--rm", "-i", "--network", "host",
		"-v", p.dir + ":/proj", "-w", "/proj",
		"-e", "COLUMNS=80", "-e", "PARITY_DSN=" + p.dsn,
	}
	for _, k := range sortedKeys(env) {
		docker = append(docker, "-e", k+"="+env[k])
	}
	docker = append(docker, parityImage, "python", "manage.py")
	cmd := exec.Command("docker", append(docker, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		p.t.Fatalf("running Django %v: %v\n%s", args, err, out.String())
	}
	return out.String(), code
}

type parityGoProject struct {
	t    *testing.T
	dir  string
	bin  string
	dsn  string
	repo string
}

func newParityGoProject(t *testing.T, repo string) *parityGoProject {
	t.Helper()
	_, info := dbtest.Open(t, "pg18")
	dir := t.TempDir()
	if err := copyTree(filepath.Join(repo, "parity", "goproj"), dir); err != nil {
		t.Fatal(err)
	}
	gomod := filepath.Join(dir, "go.mod")
	b, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(gomod, []byte(strings.ReplaceAll(string(b), "=> ../..", "=> "+repo)), 0o644); err != nil {
		t.Fatal(err)
	}
	// Django's harness ships an empty migrations package per app
	// (__init__.py); the gormgate equivalent is a migrations.go that
	// registers the package, plus the generated file that imports them.
	var imports []string
	for _, app := range []string{"auth_app", "blog"} {
		mdir := filepath.Join(dir, app, "migrations")
		if err := os.MkdirAll(mdir, 0o755); err != nil {
			t.Fatal(err)
		}
		src := "// Package migrations holds the migrations of app " + strconv.Quote(app) + ".\npackage migrations\n\nimport m \"github.com/ctolon/gormgate/migrations\"\n\nfunc init() { m.RegisterPackage(" + strconv.Quote(app) + ") }\n"
		if err := os.WriteFile(filepath.Join(mdir, "migrations.go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		imports = append(imports, "\t_ \"example.com/parityproj/"+app+"/migrations\"")
	}
	link := "// Code generated by gormgate. DO NOT EDIT.\n\npackage main\n\nimport (\n" + strings.Join(imports, "\n") + "\n)\n"
	if err := os.WriteFile(filepath.Join(dir, "cmd", "gorm-gate", "zz_gormgate_migrations.go"), []byte(link), 0o644); err != nil {
		t.Fatal(err)
	}
	// The binary is called manage.py so that both sides report the same
	// program name: it is the prefix of every usage line, and the width
	// left for the wrapped text depends on its length.
	return &parityGoProject{t: t, dir: dir, bin: filepath.Join(dir, "manage.py"), dsn: info.DSN, repo: repo}
}

func (p *parityGoProject) writeModels(app, src string) {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, app, "models.go"), []byte(src), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func (p *parityGoProject) writeFile(rel, src string) {
	p.t.Helper()
	path := filepath.Join(p.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		p.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

// deleteMigration removes "<app>/<name>" from the project. The next run
// rebuilds the command, so the migration leaves the compiled registry with
// its file.
func (p *parityGoProject) deleteMigration(mig string) {
	p.t.Helper()
	app, name, _ := strings.Cut(mig, "/")
	if err := os.Remove(filepath.Join(p.dir, app, "migrations", name+".go")); err != nil {
		p.t.Fatal(err)
	}
}

func (p *parityGoProject) run(stdin string, env map[string]string, args ...string) (string, int) {
	p.t.Helper()
	build := exec.Command("go", "build", "-buildvcs=false", "-o", p.bin, "./cmd/gorm-gate")
	build.Dir = p.dir
	if out, err := build.CombinedOutput(); err != nil {
		p.t.Fatalf("building the parity gorm-gate command: %v\n%s", err, out)
	}
	cmd := exec.Command(p.bin, args...)
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "PARITY_DSN="+p.dsn, "GORMGATE_COLORS=nocolor", "COLUMNS=80")
	for _, k := range sortedKeys(env) {
		cmd.Env = append(cmd.Env, k+"="+env[k])
	}
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		p.t.Fatalf("running gormgate %v: %v\n%s", args, err, out.String())
	}
	return out.String(), code
}

// sortedKeys returns the keys of env in a deterministic order.
func sortedKeys(env map[string]string) []string { return slices.Sorted(maps.Keys(env)) }

// djangoArgv translates a gormgate command line into Django's. The
// commands, their positionals and most of their options are spelled the
// same; these are the ones that are not, and the table is the executable
// half of the mapping in docs/from-django.md.
func djangoArgv(argv []string) []string {
	out := make([]string, 0, len(argv)+2)
	for _, a := range argv {
		switch a {
		case "--no-input", "-y":
			out = append(out, "--noinput")
		case "-q":
			out = append(out, "--verbosity", "0")
		case "-v":
			out = append(out, "--verbosity", "2")
		case "-vv", "-vvv":
			out = append(out, "--verbosity", "3")
		default:
			out = append(out, a)
		}
	}
	return out
}

// migrationsOnDisk lists the migrations of a project as "<app>/<name>",
// sorted, with the file extension dropped so that a Python and a Go
// project are comparable.
func migrationsOnDisk(t *testing.T, root, ext string) []string {
	t.Helper()
	var out []string
	for _, app := range []string{"auth_app", "blog"} {
		entries, err := os.ReadDir(filepath.Join(root, app, "migrations"))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ext) {
				continue
			}
			base := strings.TrimSuffix(name, ext)
			// The package files: Django's empty __init__.py and the
			// migrations.go that registers the gormgate package.
			if base == "__init__" || base == "migrations" {
				continue
			}
			out = append(out, app+"/"+base)
		}
	}
	slices.Sort(out)
	return out
}

func (p *djangoProject) migrations() []string {
	return migrationsOnDisk(p.t, p.dir, ".py")
}

func (p *parityGoProject) migrations() []string {
	return migrationsOnDisk(p.t, p.dir, ".go")
}

// planFacts reduces a "migrate --plan" output to what the two sides must
// agree on: the migrations to run, in order, and what each of them does.
// The wording is gormgate's own, so every line is lower-cased before it is
// compared; the headings are dropped.
func planFacts(out string) []string {
	var facts []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "",
			strings.EqualFold(line, "planned operations:"),
			strings.EqualFold(line, "no planned migration operations"),
			strings.EqualFold(line, "no planned migration operations."):
			continue
		}
		facts = append(facts, lowerFirst(line))
	}
	return facts
}

// lowerFirst lower-cases the first letter of s, which is the whole of the
// difference between Django's wording of an operation and gormgate's.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToLower(r[0])
	return string(r)
}
