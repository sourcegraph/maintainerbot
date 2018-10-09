package tasks_test

import (
	"context"
	"os"

	"github.com/sourcegraph/maintainerbot"
	"github.com/sourcegraph/maintainerbot/tasks"
)

func ExampleCongratulator() {
	ghc := maintainerbot.NewGitHubClient(os.Getenv("GITHUB_TOKEN"), 0)
	bot := maintainerbot.New("rails", "rails", os.Getenv("GITHUB_TOKEN"))
	task := tasks.NewCongratulator(ghc, "Congrats on your first PR, @{{ .Username }}!")
	bot.RegisterTask(task)
	bot.Run(context.TODO())
}
