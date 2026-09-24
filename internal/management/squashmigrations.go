package management

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/optimizer"
	m "github.com/ctolon/gormgate/migrations"
)

// squashOptions are squashmigrations' options, apart from the migrations
// it is told to squash.
type squashOptions struct {
	interactive  bool
	noOptimize   bool
	squashedName string
	header       bool
}

// newSquashMigrationsCmd builds the squashmigrations command.
func newSquashMigrationsCmd(s *state) *cobra.Command {
	var (
		noInput  bool
		noHeader bool
		o        squashOptions
	)
	cmd := &cobra.Command{
		Use:   "squashmigrations app [start] migration",
		Short: "fold a run of migrations into a single one",
		Long: "Replace an app's migrations, from the beginning or from start, up to\n" +
			"and including migration, with one migration that has the same effect.",
		Args: usageArgs(cobra.RangeArgs(2, 3)),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.runChecks(); err != nil {
				return err
			}
			app, start, name := args[0], "", args[1]
			if len(args) == 3 {
				start, name = args[1], args[2]
			}
			o.interactive = !noInput
			o.header = !noHeader
			return squashMigrations(s.ctx, app, start, name, o)
		},
	}
	f := cmd.Flags()
	f.BoolVar(&o.noOptimize, "no-optimize", false, "keep every operation instead of folding them together")
	f.BoolVarP(&noInput, "no-input", "y", false, "do not prompt; assume the answer that carries on")
	f.StringVar(&o.squashedName, "squashed-name", "", "name for the new migration")
	f.BoolVar(&noHeader, "no-header", false, "omit the generated-by comment at the top of the file")
	return cmd
}

// findMigration resolves a migration from a name prefix and turns the
// loader's failures into command errors. notFoundSuffix is appended to the
// "cannot find a migration" message; only sqlmigrate adds one.
func findMigration(l *loader.Loader, app, name, notFoundSuffix string) (*m.Migration, error) {
	mig, err := l.GetMigrationByPrefix(app, name)
	var amb *m.AmbiguityError
	var ke *loader.MigrationNotFoundError
	switch {
	case errors.As(err, &amb):
		return nil, Errorf("more than one migration matches '%s' in app '%s'. Please be more specific", name, app)
	case errors.As(err, &ke):
		return nil, Errorf("cannot find a migration matching '%s' from app '%s'%s", name, app, notFoundSuffix)
	}
	return mig, err
}

// readLine is Python's input(prompt): the prompt goes to stdout.
func readLine(ctx *Context, prompt string, in *bufio.Reader) (string, error) {
	io.WriteString(ctx.Stdout.Writer(), prompt)
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("input ended")
	}
	return strings.TrimRight(line, "\r\n"), nil
}

// squashMigrations folds an app's migrations up to name into one.
func squashMigrations(ctx *Context, app, startName, name string, o squashOptions) error {
	verbosity := ctx.Verbosity
	interactive := o.interactive
	squashedName := o.squashedName
	l, err := ctx.Project.checkMigratedApp(func() (*loader.Loader, error) { return ctx.Project.Loader(nil, false) }, app, " (so squashmigrations on it makes no sense)")
	if err != nil {
		return err
	}
	mig, err := findMigration(l, app, name, "")
	if err != nil {
		return err
	}
	fp, err := l.Graph.ForwardsPlan(mig.Key())
	if err != nil {
		return err
	}
	var toSquash []*m.Migration
	for _, k := range fp {
		if k.App == mig.App {
			n, _ := l.GetMigration(k.App, k.Name)
			toSquash = append(toSquash, n)
		}
	}
	var start *m.Migration
	if startName != "" {
		if start, err = findMigration(l, app, startName, ""); err != nil {
			return err
		}
		idx := -1
		for i, x := range toSquash {
			if x.Key() == start.Key() {
				idx = i
				break
			}
		}
		if idx < 0 {
			return Errorf("the migration '%s' cannot be found. Maybe it comes after the migration '%s'?\nHave a look at:\n  %s showmigrations %s\nto debug this issue", start, mig, ctx.Invocation(), app)
		}
		toSquash = toSquash[idx:]
	}
	if verbosity > 0 || interactive {
		ctx.Stdout.Println(ctx.Style.MigrateHeading("will squash:"))
		for _, x := range toSquash {
			ctx.Stdout.Println(" - " + x.Name)
		}
		if interactive {
			in := bufio.NewReader(ctx.Stdin)
			answer := ""
			for answer == "" || !strings.Contains("yn", answer) {
				a, err := readLine(ctx, "Do you wish to proceed? [y/N] ", in)
				if err != nil {
					return err
				}
				if a == "" {
					answer = "n"
					break
				}
				answer = strings.ToLower(a[:1])
			}
			if answer != "y" {
				return nil
			}
		}
	}
	var ops []m.Operation
	deps := map[m.Key]bool{}
	var depOrder []m.Key
	for i, s := range toSquash {
		ops = append(ops, s.Operations...)
		for _, d := range s.Dependencies {
			if d.App != s.App || i == 0 {
				if !deps[d] {
					deps[d] = true
					depOrder = append(depOrder, d)
				}
			}
		}
	}
	newOps := ops
	if o.noOptimize {
		if verbosity > 0 {
			ctx.Stdout.Println(ctx.Style.MigrateHeading("(Skipping optimization.)"))
		}
	} else {
		if verbosity > 0 {
			ctx.Stdout.Println(ctx.Style.MigrateHeading("optimizing"))
		}
		newOps = optimizer.Optimize(ops, mig.App)
		if verbosity > 0 {
			if len(newOps) == len(ops) {
				ctx.Stdout.Println("  no optimizations possible")
			} else {
				ctx.Stdout.Println(fmt.Sprintf("  Optimized from %d operations to %d operations.", len(ops), len(newOps)))
			}
		}
	}
	var replaces []m.Key
	for _, s := range toSquash {
		replaces = append(replaces, s.Key())
	}
	squashed := &m.Migration{App: app, Dependencies: depOrder, Operations: newOps, Replaces: replaces}
	if startName != "" {
		if squashedName != "" {
			prefix, _, _ := strings.Cut(start.Name, "_")
			squashed.Name = prefix + "_" + squashedName
		} else {
			squashed.Name = start.Name + "_squashed_" + mig.Name
		}
	} else {
		if squashedName == "" {
			squashedName = "squashed_" + mig.Name
		}
		squashed.Name = "0001_" + squashedName
		squashed.Initial = m.Ptr(true)
	}
	w, path, err := migrationWriter(ctx.Project, squashed, o.header)
	if err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		return Errorf("migration %s already exists. Use a different name", squashed.Name)
	}
	content, err := w.AsString()
	if err != nil {
		return err
	}
	if err := writeFile(path, content); err != nil {
		return err
	}
	if verbosity > 0 {
		ctx.Stdout.Println(ctx.Style.MigrateHeading("created squashed migration "+path) + "\n" +
			"  You should commit this migration but leave the old ones in place;\n" +
			"  the new migration will be used for new installs. Once you are sure\n" +
			"  all instances of the codebase have applied the migrations you squashed,\n" +
			"  you can delete them.")
		if w.NeedsManualPorting {
			ctx.Stdout.Println(ctx.Style.MigrateHeading("manual porting required") + "\n" +
				"  Your migrations contained functions that must be manually copied over,\n" +
				"  as we could not safely copy their implementation.\n" +
				"  See the comment at the top of the squashed migration for details.")
		}
	}
	return nil
}
