package maintainerbot_test

import (
	"context"
	"os"
	"time"

	"github.com/sourcegraph/maintainerbot"
	"github.com/sourcegraph/maintainerbot/tasks"
)

func Example() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	token := os.Getenv("GITHUB_TOKEN")
	ghc := maintainerbot.NewGitHubClient(token, 0)
	bot := maintainerbot.New("golang", "go", token)
	spreadsheetURL := "https://docs.google.com/spreadsheets/d/<key>/export?format=csv&sheet=0"
	cla := tasks.NewCLAChecker(ghc, "http://example.com/sign-cla", tasks.NewSpreadsheetFetcher(spreadsheetURL))
	cla.StartFetch(ctx)
	congrats := tasks.NewCongratulator(ghc, "Congrats, @{{ .Username }}!")
	bot.RegisterTask(cla)
	bot.RegisterTask(congrats)
	bot.Run(ctx)
}
