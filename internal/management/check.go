package management

import (
	"fmt"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

// failLevel is the message level at or above which check exits non-zero.
// It is a pflag.Value so that a bad level is rejected by the parser, with
// the accepted ones named in the message.
type failLevel int

var failLevelNames = []struct {
	name  string
	level Level
}{
	{"critical", LevelCritical},
	{"error", LevelError},
	{"warning", LevelWarning},
	{"info", LevelInfo},
	{"debug", LevelDebug},
}

func (f *failLevel) String() string {
	for _, l := range failLevelNames {
		if Level(*f) == l.level {
			return l.name
		}
	}
	return "error"
}

func (f *failLevel) Set(s string) error {
	for _, l := range failLevelNames {
		if strings.EqualFold(s, l.name) {
			*f = failLevel(l.level)
			return nil
		}
	}
	names := make([]string, len(failLevelNames))
	for i, l := range failLevelNames {
		names[i] = l.name
	}
	return fmt.Errorf("must be one of %s", strings.Join(names, ", "))
}

func (f *failLevel) Type() string { return "level" }

// newCheckCmd builds the check command. It runs the system checks on their
// own, so it does not ask the root to run them first.
func newCheckCmd(s *state) *cobra.Command {
	var (
		tags      []string
		databases []string
		listTags  bool
		deploy    bool
	)
	level := failLevel(LevelError)

	cmd := &cobra.Command{
		Use:   "check [app ...]",
		Short: "report problems with the project without changing anything",
		Long: "Run the system checks and report what they find. With no apps named,\n" +
			"every app is checked.",
		Args: usageArgs(cobra.ArbitraryArgs),
		RunE: func(c *cobra.Command, args []string) error {
			if listTags {
				s.ctx.Stdout.Println(strings.Join(tagsAvailable(), "\n"))
				return nil
			}
			for _, label := range args {
				if s.ctx.Project.App(label) == nil {
					return Errorf("no app named %q; is it in the project's Apps?", label)
				}
			}
			if i := slices.IndexFunc(tags, func(t string) bool { return !tagExists(t) }); i >= 0 {
				return Errorf("no check carries the %q tag", tags[i])
			}
			for _, alias := range databases {
				if err := s.checkDatabase(alias); err != nil {
					return err
				}
			}
			return runChecks(s.ctx, checkOptions{
				AppLabels:        args,
				Tags:             tags,
				Databases:        databases,
				Deploy:           deploy,
				DisplayNumErrors: true,
				FailLevel:        Level(level),
			})
		},
	}
	f := cmd.Flags()
	f.StringArrayVarP(&tags, "tag", "t", nil, "run only the checks carrying this tag; repeatable")
	f.BoolVar(&listTags, "list-tags", false, "list the tags the checks carry")
	f.BoolVar(&deploy, "deploy", false, "include the deployment checks")
	f.Var(&level, "fail-level", "exit non-zero at this message level or above")
	f.StringArrayVar(&databases, "database", nil,
		"run the checks that need a connection against this database; repeatable")
	return cmd
}
