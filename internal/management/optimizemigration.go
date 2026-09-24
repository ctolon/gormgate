package management

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/optimizer"
	m "github.com/ctolon/gormgate/migrations"
)

// newOptimizeMigrationCmd builds the optimizemigration command.
func newOptimizeMigrationCmd(s *state) *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "optimizemigration app migration",
		Short: "rewrite one migration with fewer operations",
		Long: "Fold a migration's operations into the smallest set that has the same\n" +
			"effect, and write it back in place.",
		Args: usageArgs(cobra.ExactArgs(2)),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.runChecks(); err != nil {
				return err
			}
			return optimizeMigration(s.ctx, args[0], args[1], check)
		},
	}
	cmd.Flags().BoolVar(&check, "check", false,
		"exit non-zero if the migration can be optimized, without writing it")
	return cmd
}

// optimizeMigration optimizes one migration and writes it back.
func optimizeMigration(ctx *Context, app, name string, check bool) error {
	l, err := ctx.Project.checkMigratedApp(func() (*loader.Loader, error) { return ctx.Project.Loader(nil, false) }, app, "")
	if err != nil {
		return err
	}
	mig, err := findMigration(l, app, name, "")
	if err != nil {
		return err
	}
	newOps := optimizer.Optimize(mig.Operations, mig.App)
	if len(newOps) == len(mig.Operations) {
		if ctx.Verbosity > 0 {
			ctx.Stdout.Println("no optimizations possible")
		}
		return nil
	}
	if ctx.Verbosity > 0 {
		ctx.Stdout.Println(fmt.Sprintf("optimizing %d operations into %d", len(mig.Operations), len(newOps)))
	}
	if check {
		return Silent(ExitError, "the migration can be optimized")
	}
	mig = mig.Clone()
	mig.Operations = newOps
	w, path, err := migrationWriter(ctx.Project, mig, true)
	if err != nil {
		return err
	}
	content, err := w.AsString()
	if err != nil {
		return err
	}
	if w.NeedsManualPorting {
		if len(mig.Replaces) > 0 {
			return Errorf("this migration needs manual porting but is already a squashed migration; turn it into an ordinary migration first")
		}
		opt := &m.Migration{App: app, Name: mig.Name + "_optimized", Dependencies: mig.Dependencies, Operations: newOps, Replaces: []m.Key{mig.Key()}}
		w, path, err = migrationWriter(ctx.Project, opt, true)
		if err != nil {
			return err
		}
		if content, err = w.AsString(); err != nil {
			return err
		}
		if ctx.Verbosity > 0 {
			ctx.Stdout.Println(ctx.Style.MigrateHeading("manual porting required") + "\n" +
				"  the migration holds functions whose bodies could not be copied safely;\n" +
				"  the comment at the top of the optimized migration says which")
		}
	}
	if err := writeFile(path, content); err != nil {
		return err
	}
	if ctx.Verbosity > 0 {
		ctx.Stdout.Println(ctx.Style.MigrateHeading("optimized " + path))
	}
	return nil
}
