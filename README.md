# maintainerbot

Maintainerbot is designed to make it easy to create custom bots for interacting
with Github repositories. The core of Maintainerbot is `maintner`, a Go program
that creates an in-memory representation of your Github repository. Removing the
Github API (and the need to handle rate limits, and paging) is a great way to
make creating bots easy.

### Usage

Here's a bot that can check whether users have signed the CLA, against a list of
usernames stored in a spreadsheet.

```go
ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
defer cancel()

token := os.Getenv("GITHUB_TOKEN")
ghc := maintainerbot.NewGitHubClient(token, 0)
bot := maintainerbot.New("rails", "rails", token)
spreadsheetURL := "https://docs.google.com/spreadsheets/d/<key>/export?format=csv&sheet=0"
cla := tasks.NewCLAChecker(ghc, "http://example.com/sign-cla", tasks.NewSpreadsheetFetcher(spreadsheetURL))
cla.StartFetch(ctx)
bot.RegisterTask(cla)
bot.Run(ctx)
```

Other task types are available in the [tasks][tasks] package.

[tasks]: https://godoc.org/github.com/sourcegraph/maintainerbot/tasks

### Installation

```
go get -u github.com/sourcegraph/maintainerbot
```

### More Docs/Examples

Check out the [godoc][godoc] for a comprehensive list of examples.

[godoc]: https://godoc.org/github.com/sourcegraph/maintainerbot
