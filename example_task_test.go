package maintainerbot_test

import (
	"context"
	"os"
	"strings"

	maintainerbot "github.com/sourcegraph/maintainerbot"
	"golang.org/x/build/maintner"
)

type docTask struct{}

// Do labels each GitHub issue containing the word "doc" in the title with the
// "Documentation" label.
func (d *docTask) Do(ctx context.Context, repo *maintner.GitHubRepo) error {
	return repo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
		if gi.Closed || gi.PullRequest || !strings.Contains(gi.Title, "doc") || gi.HasLabel("Documentation") {
			return nil
		}
		// Issue needs a "documentation" label, add it here.
		return nil
	})
}

func ExampleTask() {
	d := &docTask{}
	bot := maintainerbot.New("rails", "rails", os.Getenv("GITHUB_TOKEN"))
	bot.RegisterTask(d)
	bot.Run(context.TODO())
}
