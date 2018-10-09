// Modified code from golang.org/x/build/cmd/gopherbot. License for gopherbot is
// here:
//
// Copyright 2017 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

// Package maintainerbot contains tools for running automated tasks on a Github repo.
//
// Call maintainerbot.New() to create a new bot, then register tasks on it with
// bot.RegisterTask(). Finally, bot.Run() will run the tasks in a loop, calling
// each Task periodically. The Task can do whatever it needs to do to update the
// repository as it sees fit.
package maintainerbot

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/build/maintner"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

// Task is any periodic task that you would like to run on the repository.
type Task interface {
	Do(ctx context.Context, repo *maintner.GitHubRepo) error
}

type Bot struct {
	// Directory for caching local data about issues and pull requests.
	// Defaults to $HOME/var/maintainerbot.
	DataDir string
	// Interval between queries to send to GitHub. Defaults to 720ms, which
	// works out to 5000 queries per hour.
	GitHubRateLimit time.Duration

	corpus *maintner.Corpus
	repo   *maintner.GitHubRepo

	owner, repoName, token string

	tasks  []Task
	taskMu sync.Mutex
}

// RegisterTask registers t with the bot. When the Bot is running, t will be
// called periodically with the latest repo contents.
func (b *Bot) RegisterTask(t Task) {
	b.tasks = append(b.tasks, t)
}

// New creates a new Bot.
func New(owner, repo, token string) *Bot {
	return &Bot{
		DataDir:  filepath.Join(os.Getenv("HOME"), "var", "maintainerbot"),
		owner:    owner,
		repoName: repo,
		token:    token,
	}
}

func (b *Bot) doTasks(ctx context.Context) error {
	b.taskMu.Lock()
	defer b.taskMu.Unlock()
	for i := range b.tasks {
		err := b.tasks[i].Do(ctx, b.repo)
		if err != nil {
			return err
		}
	}
	return nil
}

// Run calls each registered task in turn with the updated contents of the
// GitHub repository, until the context is canceled. If a task returns a
// non-zero error, it is logged to the console.
func (b *Bot) Run(ctx context.Context) {
	b.initCorpus(ctx)

	ticker := time.NewTicker(15 * time.Second)
	for ; true; <-ticker.C {
		t0 := time.Now()
		err := b.doTasks(ctx)
		if err != nil {
			log.Print(err)
		}
		botDur := time.Since(t0)
		log.Printf("maintainerbot ran in %v", botDur.Round(time.Millisecond))
		for {
			t0 := time.Now()
			err := b.corpus.Sync(ctx)
			if err != nil {
				if err == maintner.ErrSplit {
					log.Print("Corpus out of sync. Re-fetching corpus.")
					b.initCorpus(ctx)
				} else {
					log.Printf("corpus.Sync: %v; sleeping 15s", err)
					time.Sleep(15 * time.Second)
					continue
				}
			}
			log.Printf("got corpus update after %v", time.Since(t0).Round(time.Millisecond))
			break
		}
	}
}

func (b *Bot) initCorpus(ctx context.Context) error {
	corpus := new(maintner.Corpus)
	logger := maintner.NewDiskMutationLogger(b.DataDir)
	corpus.EnableLeaderMode(logger, b.DataDir)
	corpus.TrackGitHub(b.owner, b.repoName, b.token)
	rateLimit := b.GitHubRateLimit
	if rateLimit == 0 {
		rateLimit = time.Hour / 5000
	}
	limit := rate.Every(rateLimit)
	corpus.SetGitHubLimiter(rate.NewLimiter(limit, 20))

	t0 := time.Now()
	if err := corpus.Initialize(ctx, logger); err != nil {
		// TODO: if Initialize only partially syncs the data, we need to delete
		// whatever files it created, since Github returns events newest first
		// and we use the issue updated dates to check whether we need to keep
		// syncing.
		log.Fatal(err)
	}
	initDur := time.Since(t0)
	runtime.GC()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	log.Printf("Loaded data in %v. Memory: %v MB (%v bytes)", initDur.Round(time.Millisecond), ms.HeapAlloc>>20, ms.HeapAlloc)

	repo := corpus.GitHub().Repo(b.owner, b.repoName)
	if repo == nil {
		log.Fatalf("Failed to find %s/%s repo in Corpus.", b.owner, b.repoName)
	}

	b.corpus = corpus
	b.repo = repo
	return nil
}

type limitTransport struct {
	limiter *rate.Limiter
	base    http.RoundTripper
}

func (t limitTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	limiter := t.limiter
	// NOTE(cbro): limiter should not be nil, but check defensively.
	if limiter != nil {
		if err := limiter.Wait(r.Context()); err != nil {
			return nil, err
		}
	}
	return t.base.RoundTrip(r)
}

// NewGitHubClient creates a new GitHub client for the given token. rateLimit is
// the duration between requests; a rate limit of 0 defaults to 5000 requests
// per hour.
func NewGitHubClient(token string, rateLimit time.Duration) *github.Client {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	var transport http.RoundTripper = tc.Transport
	if rateLimit == 0 {
		rateLimit = time.Hour / 5000
	}
	limit := rate.Every(rateLimit)
	transport = limitTransport{rate.NewLimiter(limit, 20), tc.Transport}
	httpClient := &http.Client{Transport: transport}
	return github.NewClient(httpClient)
}
