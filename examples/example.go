// Modified code from golang.org/x/build/cmd/gopherbot. License for gopherbot is
// here:
//
// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// The sgbot command runs automated tasks against the Sourcegraph Github repo.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/go-github/github"
	"github.com/sourcegraph/maintainerbot"
	"github.com/sourcegraph/maintainerbot/tasks"
)

func getGithubToken() (string, error) {
	if token, ok := os.LookupEnv("GITHUB_TOKEN"); ok {
		return token, nil
	}
	slurp, err := os.ReadFile(*githubTokenFile)
	if err != nil {
		return "", err
	}
	f := strings.SplitN(strings.TrimSpace(string(slurp)), ":", 2)
	if len(f) != 2 || f[0] == "" || f[1] == "" {
		return "", fmt.Errorf("Expected token %q to be of form <username>:<token>", slurp)
	}
	return f[1], nil
}

func getGithubClient(rateLimit time.Duration) (*github.Client, error) {
	token, err := getGithubToken()
	if err != nil {
		return nil, err
	}
	return maintainerbot.NewGitHubClient(token, rateLimit), nil
}

var dataDir = flag.String("data-dir", filepath.Join(os.Getenv("HOME"), "var", "sgbot"), "Local directory to write protobuf files to (default $HOME/var/sgbot)")

// Github allows 5000 queries per hour which is roughly one query every 720ms,
// we set this as the default value. A lower Duration means you can complete
// queries more quickly, however, you may hit the rate limit and get blocked
// until the hour limit (which Github applies on a rolling basis) expires.
var githubRateLimit = flag.Duration("github-rate", time.Hour/5000, "Rate to limit GitHub requests (amount of time to pass between requests)")
var githubTokenFile = flag.String("github-token-file", filepath.Join(os.Getenv("HOME"), "keys", "github-sgbot"), `File to load Github token from. File should be of form <username>:<token>`)
var spreadsheetURL = flag.String("spreadsheet-url", "", "Spreadsheet URL for loading contributors")
var claURL = flag.String("cla-url", "", "URL where users can sign the CLA")
var githubRepo = flag.String("repo", "sourcegraph/sourcegraph", "Github repo to watch, in owner/repo-name format")

func init() {
	flag.Usage = func() {
		os.Stderr.WriteString("sgbot checks CLA's and performs other automated tasks.\n\n")
		flag.PrintDefaults()
	}
}

func main() {
	flag.Parse()
	if *spreadsheetURL == "" {
		fmt.Fprintf(os.Stderr, "Please provide a spreadsheet URL\n")
		flag.Usage()
		os.Exit(2)
	}
	if *claURL == "" {
		fmt.Fprintf(os.Stderr, "Please provide a CLA URL\n")
		flag.Usage()
		os.Exit(2)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	token, err := getGithubToken()
	if err != nil {
		log.Fatal(err)
	}
	ghc, err := getGithubClient(*githubRateLimit / 3)
	if err != nil {
		log.Fatal(err)
	}
	splits := strings.SplitN(*githubRepo, "/", 2)
	if len(splits) != 2 || splits[1] == "" {
		log.Fatalf("Invalid github repo: %s. Should be 'owner/repo'", *githubRepo)
	}
	bot := maintainerbot.New(splits[0], splits[1], token)
	bot.DataDir = *dataDir
	bot.GitHubRateLimit = *githubRateLimit / 3 * 2
	spreadsheetFetcher := tasks.NewSpreadsheetFetcher(*spreadsheetURL)
	spreadsheetFetcher.ColumnName = "GitHub Handle"
	cla := tasks.NewCLAChecker(ghc, *claURL, spreadsheetFetcher)
	cla.CanSkipCLA = func(pr *github.PullRequest, files []*github.CommitFile) bool {
		if pr == nil || files == nil {
			panic("nil PR or nil files in CanSkipCLA check; can't compare")
		}
		if pr.GetAdditions()+pr.GetDeletions() <= 15 {
			return true
		}
		hasOnlyMarkdownFiles := true
		for i := range files {
			if !strings.HasSuffix(files[i].GetFilename(), ".md") {
				hasOnlyMarkdownFiles = false
				break
			}
		}
		return hasOnlyMarkdownFiles
	}
	cla.StartFetch(ctx)
	bot.RegisterTask(cla)
	congratulator := tasks.NewCongratulator(ghc, `Thanks for the contribution, @{{ .Username }}!

You should receive feedback on your pull request within a few days. If you haven't already, please read through <a href="https://github.com/sourcegraph/sourcegraph/blob/master/CONTRIBUTING.md"> the contributing guide</a>, and ensure that you've <a href="`+*claURL+`">signed the CLA</a>.

Did you run into any issues when creating this PR? Please describe them in <a href="https://github.com/sourcegraph/sourcegraph/issues/new/choose">an issue</a> so we can make the experience better for the next contributor.
`)
	bot.RegisterTask(congratulator)
	bot.Run(ctx)
}
