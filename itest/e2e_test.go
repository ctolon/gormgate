//go:build integration

package itest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctolon/gormgate/itest/internal/dbtest"
)

// project is a copy of examples/blogproj driven through its gorm-gate
// command, exactly as a user would run it.
type project struct {
	t    *testing.T
	dir  string
	dsn  string
	bin  string
	repo string
}

func repoRoot(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(wd)
}

func newProject(t *testing.T, dsn string) *project {
	t.Helper()
	repo := repoRoot(t)
	dir := t.TempDir()
	src := filepath.Join(repo, "examples", "blogproj")
	if err := copyTree(src, dir); err != nil {
		t.Fatal(err)
	}
	gomod := filepath.Join(dir, "go.mod")
	b, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatal(err)
	}
	fixed := strings.ReplaceAll(string(b), "=> ../..", "=> "+repo)
	if err := os.WriteFile(gomod, []byte(fixed), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &project{t: t, dir: dir, dsn: dsn, repo: repo, bin: filepath.Join(dir, "gorm-gate")}
	p.build()
	return p
}

func copyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, b, info.Mode())
	})
}

// build compiles the gorm-gate command, which is what makes newly written
// migration files part of the program.
func (p *project) build() {
	p.t.Helper()
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", p.bin, "./cmd/gorm-gate")
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	if out, err := cmd.CombinedOutput(); err != nil {
		p.t.Fatalf("building the gorm-gate command: %v\n%s", err, out)
	}
}

type result struct {
	stdout, stderr string
	code           int
}

// run rebuilds the gorm-gate command and runs it with args and stdin.
func (p *project) run(stdin string, args ...string) result {
	p.t.Helper()
	p.build()
	cmd := exec.Command(p.bin, args...)
	cmd.Dir = p.dir
	cmd.Env = append(os.Environ(), "BLOGPROJ_DSN="+p.dsn, "GORMGATE_COLORS=nocolor")
	cmd.Stdin = strings.NewReader(stdin)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		p.t.Fatalf("running gorm-gate %v: %v", args, err)
	}
	return result{stdout: out.String(), stderr: errb.String(), code: code}
}

// mustRun fails the test when the command exits non-zero.
func (p *project) mustRun(stdin string, args ...string) result {
	p.t.Helper()
	r := p.run(stdin, args...)
	if r.code != 0 {
		p.t.Fatalf("gorm-gate %v exited %d\nstdout:\n%s\nstderr:\n%s", args, r.code, r.stdout, r.stderr)
	}
	return r
}

func (p *project) edit(rel string, replacements ...[2]string) {
	p.t.Helper()
	path := filepath.Join(p.dir, rel)
	b, err := os.ReadFile(path)
	if err != nil {
		p.t.Fatal(err)
	}
	s := string(b)
	for _, r := range replacements {
		if !strings.Contains(s, r[0]) {
			p.t.Fatalf("%s: %q not found", rel, r[0])
		}
		s = strings.ReplaceAll(s, r[0], r[1])
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		p.t.Fatal(err)
	}
}

func wantLines(t *testing.T, what, got string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("%s: missing %q in:\n%s", what, w, got)
		}
	}
}

func TestE2E(t *testing.T) {
	db, info := dbtest.Open(t, "pg18")
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	p := newProject(t, pgDSN(t, info))

	// help
	r := p.mustRun("")
	wantLines(t, "help", r.stdout, "Available Commands:", "makemigrations", "squashmigrations")

	// version
	r = p.mustRun("", "version")
	if strings.TrimSpace(r.stdout) == "" {
		t.Errorf("version printed nothing")
	}

	// unknown command
	r = p.run("", "migratte")
	if r.code != 2 {
		t.Errorf("unknown command exit = %d, want 2", r.code)
	}
	wantLines(t, "unknown command", r.stderr, `unknown command "migratte"; did you mean "migrate"?`, "run 'gorm-gate help' for usage")

	// makemigrations without app labels makes nothing for apps that have
	// no migrations package yet (as in Django).
	r = p.mustRun("", "makemigrations")
	wantLines(t, "makemigrations", r.stdout, "no changes detected")

	// initial migrations
	r = p.mustRun("", "makemigrations", "auth", "blog")
	wantLines(t, "makemigrations auth blog", r.stdout,
		"created auth/migrations/0001_initial.go", "  + create model User",
		"created blog/migrations/0001_initial.go", "  + create model Post", "  + create model Comment")
	for _, f := range []string{"auth/migrations/0001_initial.go", "blog/migrations/0001_initial.go", "cmd/gorm-gate/zz_gormgate_migrations.go"} {
		if _, err := os.Stat(filepath.Join(p.dir, f)); err != nil {
			t.Fatalf("expected %s: %v", f, err)
		}
	}

	// --check before migrating reports unapplied migrations.
	r = p.run("", "migrate", "--check")
	if r.code != 1 {
		t.Errorf("migrate --check exit = %d, want 1", r.code)
	}

	// migrate --plan then migrate
	r = p.mustRun("", "migrate", "--plan")
	wantLines(t, "migrate --plan", r.stdout, "planned operations:", "auth.0001_initial", "  create model User", "blog.0001_initial")
	r = p.mustRun("", "migrate")
	wantLines(t, "migrate", r.stdout, "applying auth.0001_initial ... ok", "applying blog.0001_initial ... ok")

	// showmigrations
	r = p.mustRun("", "showmigrations")
	wantLines(t, "showmigrations", r.stdout, "auth\n [X] 0001_initial", "blog\n [X] 0001_initial")
	r = p.mustRun("", "showmigrations", "--plan")
	wantLines(t, "showmigrations --plan", r.stdout, "[X]  auth.0001_initial", "[X]  blog.0001_initial")

	// migrate is idempotent and reports nothing to apply.
	r = p.mustRun("", "migrate")
	wantLines(t, "migrate again", r.stdout, "no migrations to apply")

	// A model change that needs a one-off default: --no-input exits 3.
	p.edit("blog/models.go", [2]string{"\tBody      string    `gorm:\"type:text\"`", "\tBody      string    `gorm:\"type:text\"`\n\tViews     int       `gorm:\"not null\"`"})
	r = p.run("", "makemigrations", "blog", "--no-input")
	if r.code != 3 {
		t.Errorf("makemigrations --no-input exit = %d, want 3\n%s%s", r.code, r.stdout, r.stderr)
	}
	wantLines(t, "noinput", r.stdout, "post.views not migrated: it is impossible to add a non-nullable field without specifying a default")

	// Interactive: choose a one-off default of 0.
	r = p.mustRun("1\n0\n", "makemigrations", "blog")
	wantLines(t, "interactive", r.stdout,
		"post.views is not nullable and the rows already there need a value for it.",
		" 1) set a one-off default now, on the rows that have none",
		"enter the default as a Go expression",
		"blog/migrations/0002_post_views.go", "  + add field views to post")

	// The generated file must be valid, gofmt-clean Go.
	gen := filepath.Join(p.dir, "blog/migrations/0002_post_views.go")
	src, err := os.ReadFile(gen)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("gofmt", "-l", gen).Output(); err != nil || strings.TrimSpace(string(out)) != "" {
		t.Errorf("generated migration is not gofmt-clean: %v %s", err, out)
	}
	if !strings.Contains(string(src), "PreserveDefault: m.Ptr(false)") {
		t.Errorf("one-off default not recorded:\n%s", src)
	}

	// sqlmigrate prints the SQL of the new migration.
	r = p.mustRun("", "sqlmigrate", "blog", "0002")
	wantLines(t, "sqlmigrate", r.stdout, "BEGIN;", `ALTER TABLE "blog_posts" ADD COLUMN "views" bigint DEFAULT 0 NOT NULL`, "COMMIT;")
	r = p.mustRun("", "sqlmigrate", "blog", "0002", "--backwards")
	wantLines(t, "sqlmigrate --backwards", r.stdout, `ALTER TABLE "blog_posts" DROP COLUMN "views"`)

	// Apply it and check the data survived.
	p.mustRun("", "migrate")
	r = p.mustRun("", "showmigrations", "blog", "-v")
	wantLines(t, "showmigrations -v2", r.stdout, "[X] 0002_post_views (applied at ")

	// Unapply back to 0001 and re-apply.
	r = p.mustRun("", "migrate", "blog", "0001")
	wantLines(t, "migrate backwards", r.stdout, "unapplying blog.0002_post_views ... ok")
	p.mustRun("", "migrate")

	// --fake unapplies and re-applies without touching the schema.
	r = p.mustRun("", "migrate", "blog", "0001", "--fake")
	wantLines(t, "migrate --fake", r.stdout, "unapplying blog.0002_post_views ... faked")
	r = p.mustRun("", "migrate", "blog", "--fake")
	wantLines(t, "migrate --fake forwards", r.stdout, "applying blog.0002_post_views ... faked")

	// --fake-initial fakes initial migrations whose tables exist.
	// Unapplying auth.0001 unapplies the blog migrations depending on it,
	// so the records of all three are restored afterwards.
	p.mustRun("", "migrate", "auth", "zero", "--fake")
	r = p.mustRun("", "migrate", "auth", "--fake-initial")
	wantLines(t, "migrate --fake-initial", r.stdout, "applying auth.0001_initial ... faked")
	// Restore the blog records that were unapplied with auth's.
	r = p.mustRun("", "migrate", "blog", "--fake")
	wantLines(t, "restore blog", r.stdout, "applying blog.0001_initial ... faked", "applying blog.0002_post_views ... faked")
	r = p.mustRun("", "showmigrations", "blog")
	wantLines(t, "showmigrations after fake-initial", r.stdout, "[X] 0001_initial", "[X] 0002_post_views")

	// optimizemigration --check on an already optimal migration exits 0.
	r = p.mustRun("", "optimizemigration", "blog", "0002", "--check")
	wantLines(t, "optimizemigration", r.stdout, "no optimizations possible")

	// squashmigrations.
	r = p.mustRun("y\n", "squashmigrations", "blog", "0002")
	wantLines(t, "squashmigrations", r.stdout, "will squash:", " - 0001_initial", " - 0002_post_views",
		"Do you wish to proceed? [y/N] ", "optimizing", "created squashed migration ")
	r = p.mustRun("", "showmigrations", "blog")
	wantLines(t, "showmigrations after squash", r.stdout, "0001_squashed_0002_post_views (2 squashed migrations)")

	// migrate still reports nothing to do after squashing.
	r = p.mustRun("", "migrate")
	wantLines(t, "migrate after squash", r.stdout, "no migrations to apply")

	// inspectdb produces compilable Go.
	r = p.mustRun("", "inspectdb")
	wantLines(t, "inspectdb", r.stdout, "package models", "func (AuthUsers) TableName() string { return \"auth_users\" }")
	checkCompiles(t, p, r.stdout)

	// sqlsequencereset.
	r = p.mustRun("", "sqlsequencereset", "auth")
	wantLines(t, "sqlsequencereset", r.stdout, "setval(pg_get_serial_sequence('\"auth_users\"','id')")

	// --no-color and --force-color can't be combined.
	r = p.run("", "migrate", "--no-color", "--force-color")
	if r.code != 2 {
		t.Errorf("--no-color --force-color exit = %d, want 2", r.code)
	}
	wantLines(t, "color conflict", r.stderr, "--no-color and --force-color cannot be used together")

	// Unapply everything.
	r = p.mustRun("", "migrate", "blog", "zero")
	wantLines(t, "migrate zero", r.stdout, "unapplying blog.0001_squashed_0002_post_views ... ok")
	p.mustRun("", "migrate", "auth", "zero")
	r = p.mustRun("", "showmigrations")
	wantLines(t, "showmigrations after zero", r.stdout, " [ ] 0001_initial")
}

// checkCompiles writes inspectdb's output into a temporary package and
// builds it.
func checkCompiles(t *testing.T, p *project, source string) {
	t.Helper()
	dir := filepath.Join(p.dir, "inspected")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "build", "./inspected")
	cmd.Dir = p.dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Errorf("inspectdb output does not compile: %v\n%s\n--- source:\n%s", err, out, source)
	}
	os.RemoveAll(dir)
}

// pgDSN builds a libpq DSN for the isolated test database.
func pgDSN(t *testing.T, info dbtest.Info) string {
	t.Helper()
	if info.DSN == "" {
		t.Fatalf("dbtest returned no DSN")
	}
	return info.DSN
}
