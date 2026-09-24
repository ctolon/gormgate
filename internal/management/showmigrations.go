package management

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ctolon/gormgate/internal/graph"
	"github.com/ctolon/gormgate/internal/loader"
	"github.com/ctolon/gormgate/internal/recorder"
	m "github.com/ctolon/gormgate/migrations"
)

// showMigrations holds what the two renderings share.
type showMigrations struct{ ctx *Context }

// newShowMigrationsCmd builds the showmigrations command.
func newShowMigrationsCmd(s *state) *cobra.Command {
	var (
		database string
		asPlan   bool
	)
	cmd := &cobra.Command{
		Use:   "showmigrations [app ...]",
		Short: "show the migrations and which of them are applied",
		Long: "List each app's migrations and mark the ones applied to a database.\n" +
			"With --plan, list them in the order they would run instead.",
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(c *cobra.Command, args []string) error {
			if err := s.checkDatabase(database); err != nil {
				return err
			}
			if err := s.runChecks(); err != nil {
				return err
			}
			conn, err := s.ctx.Project.Conn(database)
			if err != nil {
				return err
			}
			sm := &showMigrations{ctx: s.ctx}
			rec := recorder.New(conn)
			if asPlan {
				return sm.showPlan(rec, args)
			}
			return sm.showList(rec, args)
		},
	}
	databaseFlag(cmd, &database)
	cmd.Flags().BoolVarP(&asPlan, "plan", "p", false,
		"list the migrations in the order they would run, with their dependencies")
	return cmd
}

func (c *showMigrations) validateAppNames(names []string) error {
	bad := false
	for _, n := range names {
		if c.ctx.Project.App(n) == nil {
			c.ctx.Stderr.Println(fmt.Sprintf("no app named %q", n))
			bad = true
		}
	}
	if bad {
		return Silent(ExitUsage, "an app named on the command line is not in the project")
	}
	return nil
}

func (c *showMigrations) showList(rec *recorder.Recorder, apps []string) error {
	l, err := loader.New(c.ctx.Project.LoaderConfig(rec, true))
	if err != nil {
		return err
	}
	records, err := rec.Records()
	if err != nil {
		return err
	}
	recorded := map[m.Key]recorder.Record{}
	for _, r := range records {
		recorded[m.Key{App: r.App, Name: r.Name}] = r
	}
	if len(apps) > 0 {
		if err := c.validateAppNames(apps); err != nil {
			return err
		}
	} else {
		apps = sortedMapKeys(l.MigratedApps)
	}
	g := l.Graph
	for _, app := range apps {
		c.ctx.Stdout.PrintlnStyled(app, c.ctx.Style.MigrateLabel)
		shown := map[m.Key]bool{}
		for _, leaf := range g.LeafNodes(app) {
			fp, err := g.ForwardsPlan(leaf)
			if err != nil {
				return err
			}
			for _, k := range fp {
				if shown[k] || k.App != app {
					continue
				}
				title := k.Name
				if n := len(g.Nodes[k].Replaces); n > 0 {
					title += fmt.Sprintf(" (%d squashed migrations)", n)
				}
				if l.Applied[k] {
					var out string
					r, isRecorded := recorded[k]
					if isRecorded {
						out = " [X] " + title
					} else {
						title += fmt.Sprintf(" run '%s migrate' to finish recording", c.ctx.Invocation())
						out = " [-] " + title
					}
					if c.ctx.Verbosity >= 2 && isRecorded {
						out += fmt.Sprintf(" (applied at %s)", r.Applied.UTC().Format("2006-01-02 15:04:05"))
					}
					c.ctx.Stdout.Println(out)
				} else {
					c.ctx.Stdout.Println(" [ ] " + title)
				}
				shown[k] = true
			}
		}
		if len(shown) == 0 {
			c.ctx.Stdout.PrintlnStyled(" (no migrations)", c.ctx.Style.Error)
		}
	}
	return nil
}

func (c *showMigrations) showPlan(rec *recorder.Recorder, apps []string) error {
	l, err := loader.New(c.ctx.Project.LoaderConfig(rec, false))
	if err != nil {
		return err
	}
	g := l.Graph
	var targets []m.Key
	if len(apps) > 0 {
		if err := c.validateAppNames(apps); err != nil {
			return err
		}
		for _, k := range g.LeafNodes("") {
			if slices.Contains(apps, k.App) {
				targets = append(targets, k)
			}
		}
	} else {
		targets = g.LeafNodes("")
	}
	var plan []m.Key
	seen := map[m.Key]bool{}
	for _, t := range targets {
		fp, err := g.ForwardsPlan(t)
		if err != nil {
			return err
		}
		for _, k := range fp {
			if !seen[k] {
				plan = append(plan, k)
				seen[k] = true
			}
		}
	}
	for _, k := range plan {
		deps := ""
		if c.ctx.Verbosity >= 2 {
			var parents []m.Key
			for p := range g.NodeMap[k].Parents {
				parents = append(parents, p.Key)
			}
			graph.SortKeys(parents)
			var out []string
			for _, p := range parents {
				out = append(out, p.App+"."+p.Name)
			}
			if len(out) > 0 {
				deps = " ... (" + strings.Join(out, ", ") + ")"
			}
		}
		mark := "[ ]"
		if l.Applied[k] {
			mark = "[X]"
		}
		c.ctx.Stdout.Println(fmt.Sprintf("%s  %s.%s%s", mark, k.App, k.Name, deps))
	}
	if len(plan) == 0 {
		c.ctx.Stdout.PrintlnStyled("(no migrations)", c.ctx.Style.Error)
	}
	return nil
}
