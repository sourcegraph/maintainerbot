// Package tasks contains a list of tasks that can be run with the bot.
//
// All tasks in this package should satisfy the maintainerbot.Task interface.
//
//     // Do labels each GitHub issue containing the word "doc" in the title with the
//     // "Documentation" label.
//     func (d *docTask) Do(ctx context.Context, repo *maintner.GitHubRepo) error {
//         return repo.ForeachIssue(func(gi *maintner.GitHubIssue) error {
//             if gi.Closed || gi.PullRequest || !strings.Contains(gi.Title, "doc") || gi.HasLabel("Documentation") {
//                 return nil
//             }
//             // Issue needs a "documentation" label, add it here.
//             return nil
//         })
//     }
package tasks

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"strings"
	"sync"
	"text/template"
	"time"

	"github.com/google/go-github/github"
	"golang.org/x/build/maintner"
)

// Congratulator congratulates new contributors, and posts a welcome message on
// the first PR they opened against the project.
//
// To avoid posting the same message multiple times, Congratulator uses a label
// ("new-contributor") to track when it has already posted a message on a given
// pull request.
type Congratulator struct {
	ghc               *github.Client
	message           *template.Template
	knownContributors map[string]bool
}

// NewCongratulator returns a new Congratulator. templ should be a message to
// post on the pull request. You can use markdown or HTML in templ, as long as
// Github will accept it.
//
// In addition, you can use the fields on CongratsData as fields in your
// template. For example, you could write "Congrats, @{{ .Username }}!" and
// Congratulator will substitute in the contributor's username when the comment
// is posted.
func NewCongratulator(ghc *github.Client, templ string) *Congratulator {
	tpl, err := template.New("congratulator").Parse(templ)
	if err != nil {
		panic(err)
	}
	return &Congratulator{
		ghc:     ghc,
		message: tpl,
	}
}

// CongratsData is the field that gets rendered into the template provided by
// NewCongratulator. More fields may be added.
type CongratsData struct {
	Username string
}

func (c *Congratulator) Do(ctx context.Context, repo *maintner.GitHubRepo) error {
	prs := make(map[string]*maintner.GitHubIssue)
	owner, repoName := repo.ID().Owner, repo.ID().Repo
	err := repo.ForeachIssue(func(gh *maintner.GitHubIssue) error {
		if c.knownContributors == nil {
			c.knownContributors = make(map[string]bool)
		}
		if gh.NotExist || !gh.PullRequest {
			return nil
		}
		username := gh.User.Login
		if c.knownContributors[username] {
			return nil
		}
		if _, ok := prs[username]; ok {
			// this person has multiple PR's; not a new contributor.
			c.knownContributors[username] = true
			delete(prs, username)
			return nil
		}
		prs[username] = gh
		return nil
	})
	buf := new(bytes.Buffer)
	for username, ghIssue := range prs {
		if ghIssue.Closed {
			c.knownContributors[username] = true
			continue
		}
		hasNewContributorLabel := false
		for _, label := range ghIssue.Labels {
			if label.Name == "new-contributor" {
				hasNewContributorLabel = true
				break
			}
		}
		if hasNewContributorLabel {
			c.knownContributors[username] = true
			continue
		}
		cdata := &CongratsData{
			Username: ghIssue.User.Login,
		}
		buf.Reset()
		err := c.message.Execute(buf, cdata)
		if err != nil {
			return err
		}
		// post label first, then post comment. if label succeeds but comment
		// fails, too bad.
		_, _, err = c.ghc.Issues.AddLabelsToIssue(ctx, owner, repoName, int(ghIssue.Number),
			[]string{"new-contributor"}) // must match label name above
		if err != nil {
			return err
		}
		comment := &github.IssueComment{
			Body: github.String(buf.String()),
		}
		_, _, err = c.ghc.Issues.CreateComment(ctx, owner, repoName, int(ghIssue.Number), comment)
		if err != nil {
			return err
		}
		c.knownContributors[username] = true
	}
	return err
}

// CLAChecker can fetch and validate that pull request authors have signed
// a CLA.
type CLAChecker struct {
	// Return true from CanSkipCLA to post a "CLA not necessary" success message
	// on matching PR's. If nil, all PR's are assumed to need a CLA.
	CanSkipCLA func(*github.PullRequest, []*github.CommitFile) bool

	signedCLAPRs       map[int32]bool
	ghc                *github.Client
	claURL             string
	contributorFetcher ContributorFetcher

	contributorsLoaded chan struct{}

	contributors  map[string]bool
	contributorMu sync.Mutex
}

// ContributorFetcher is any struct that can fetch and return a list of
// contributors that have signed the CLA. You can provide a custom
// implementation in CLAChecker, or use the provided SpreadsheetFetcher to fetch
// from a Google Sheet.
type ContributorFetcher interface {
	LoadContributors(ctx context.Context) ([]string, error)
}

func (c *CLAChecker) loadContributors(ctx context.Context) {
	sentContributors := false
	ticker := time.NewTicker(time.Minute)
	for ; true; <-ticker.C {
		contributors, err := c.contributorFetcher.LoadContributors(ctx)
		if err != nil {
			log.Println("fetch err", err)
			continue
		}
		contributorMap := make(map[string]bool, len(contributors))
		for i := range contributors {
			contributorMap[contributors[i]] = true
		}
		c.contributorMu.Lock()
		c.contributors = contributorMap
		c.contributorMu.Unlock()
		if !sentContributors {
			log.Printf("initial list of contributors loaded: " + strings.Join(contributors, ", "))
			c.contributorsLoaded <- struct{}{}
			close(c.contributorsLoaded)
			sentContributors = true
		}

		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

// Post a status to a pull request on GitHub. If "state" is "unnecessary"
// a successful status will be posted, with a separate message than the
// "success" state.
func (c *CLAChecker) postStatus(ctx context.Context, owner, repo, sha, state string) (*github.RepoStatus, error) {
	sr := &github.RepoStatus{
		State:   github.String(state),
		Context: github.String("cla-bot"),
	}
	switch state {
	case "failure":
		sr.Description = github.String("Contributor has not signed the CLA")
		sr.TargetURL = github.String(c.claURL)
	case "unnecessary":
		sr.Description = github.String("Changes do not require CLA submission")
		sr.State = github.String("success")
	case "success":
		sr.Description = github.String("Contributor has signed the CLA")
	default:
		panic("unknown state " + state)
	}
	status, _, err := c.ghc.Repositories.CreateStatus(ctx, owner, repo, sha, sr)
	return status, err
}

// Do checks whether every open pull request in the repository has been
// submitted by a user who signed the CLA. If not, Do posts a failing Status
// Check on the pull request build until the user signs the CLA.
func (c *CLAChecker) Do(ctx context.Context, repo *maintner.GitHubRepo) error {
	// loop over open PR's
	// filter out those with completed CLA's/positive checks
	// others: check user email against contributor list
	owner, repoName := repo.ID().Owner, repo.ID().Repo
	err := repo.ForeachIssue(func(gh *maintner.GitHubIssue) error {
		if gh.NotExist == true {
			return nil
		}
		if gh.PullRequest == false {
			return nil
		}
		if gh.Closed == true {
			return nil
		}
		if c.signedCLAPRs == nil {
			c.signedCLAPRs = make(map[int32]bool)
		}
		// if user is in contributors list, we can exit
		if _, ok := c.signedCLAPRs[gh.Number]; ok {
			return nil
		}
		<-c.contributorsLoaded
		c.contributorMu.Lock()
		_, ok := c.contributors[gh.User.Login]
		c.contributorMu.Unlock()
		pr, _, err := c.ghc.PullRequests.Get(ctx, owner, repoName, int(gh.Number))
		if err != nil {
			return err
		}
		files, _, err := c.ghc.PullRequests.ListFiles(ctx, owner, repoName, int(gh.Number), nil)
		if err != nil {
			return err
		}
		statuses, _, err := c.ghc.Repositories.ListStatuses(ctx, owner, repoName, pr.Head.GetSHA(), nil)
		if err != nil {
			return err
		}
		canSkipCLA := c.CanSkipCLA != nil && c.CanSkipCLA(pr, files)
		if ok || canSkipCLA {
			// fetch pull request status, add or change to success
			postStatusState := "success"
			if canSkipCLA {
				postStatusState = "unnecessary"
			}
			for i := range statuses {
				if statuses[i].GetContext() == "cla-bot" {
					state := statuses[i].GetState()
					if state == "success" {
						c.signedCLAPRs[gh.Number] = true
						return nil
					}
					_, err := c.postStatus(ctx, owner, repoName, *pr.Head.SHA, postStatusState)
					if err != nil {
						return err
					}
					log.Printf("user %q just signed CLA on PR %d, updated status to %q from previous value %q", gh.User.Login, gh.Number, postStatusState, state)
					return nil
				}
			}
			// no statuses on the pull request, post success
			status, err := c.postStatus(ctx, owner, repoName, *pr.Head.SHA, postStatusState)
			if err != nil {
				return err
			}
			log.Printf("user %q signed CLA on PR %d, posted %q status %d", gh.User.Login, gh.Number, postStatusState, status.ID)
			return nil
		}
		foundStatus := false
		for i := range statuses {
			if statuses[i].GetContext() == "cla-bot" && statuses[i].GetState() == "failure" {
				foundStatus = true
				break
			}
		}
		if foundStatus {
			return nil
		}
		// post failing status check
		status, err := c.postStatus(ctx, owner, repoName, *pr.Head.SHA, "failure")
		if err != nil {
			log.Fatal(err)
		}
		log.Printf("user %q has not signed CLA on PR %d, added status %d", gh.User.Login, gh.Number, status.ID)
		return nil
	})
	return err
}

func NewCLAChecker(ghc *github.Client, claURL string, fetcher ContributorFetcher) *CLAChecker {
	c := &CLAChecker{
		ghc: ghc, claURL: claURL,
		contributorFetcher: fetcher,
	}
	return c
}

// StartFetch will begin periodically fetching contributors from the background
// repository, until the provided context is canceled.
func (c *CLAChecker) StartFetch(ctx context.Context) {
	c.contributorsLoaded = make(chan struct{}, 1)
	go c.loadContributors(ctx)
}

// SpreadsheetFetcher fetches data from a Google spreadsheet. The
// SpreadsheetFetcher will search for the first column in the document that
// contains "Github Username" in the cell in the column's first row. For
// example, if the CSV looks like:
//
//     Name,Address,Twitch Username,Your Github Username
//     Kevin,"123 Main St",kevintwitch,kevinburke
//
// We would read usernames from the rightmost column in the document.
type SpreadsheetFetcher struct {
	sheetURL string

	// Column name to match against, defaults to "Github Username", matches are
	// case insensitive.
	ColumnName string
}

// NewSpreadsheetFetcher creates a SpreadsheetFetcher that can fetch usernames
// from the given spreadsheet.
//
// sheetURL should be the CSV url for a given Google spreadsheet, something
// like:
//
// "https://docs.google.com/spreadsheets/d/<key>/export?format=csv&sheet=0"
//
// where "key" is unique for each spreadsheet and "sheet=0" would be the first
// sheet in the document.
func NewSpreadsheetFetcher(sheetURL string) *SpreadsheetFetcher {
	return &SpreadsheetFetcher{
		sheetURL:   sheetURL,
		ColumnName: "GitHub Username",
	}
}

func downloadCSV(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "maintainerbot")
	reqctx, cancel := context.WithTimeout(ctx, 31*time.Second)
	defer cancel()
	req = req.WithContext(reqctx)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return body, nil
}

// getUsernames returns a list of email addresses from a string of bytes in CSV
// format. The first row must have a column named "email" or "Email".
func getUsernames(file []byte, columnName string) ([]string, error) {
	r := csv.NewReader(bytes.NewReader(file))
	record, err := r.Read()
	if err != nil {
		return nil, err
	}
	column := -1
	lowerColumnName := strings.ToLower(columnName)
	for i := range record {
		lower := strings.ToLower(record[i])
		if strings.Contains(lower, lowerColumnName) {
			column = i
			break
		}
	}
	if column == -1 {
		return nil, fmt.Errorf("no column named '%s'; quitting", columnName)
	}
	records, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	emails := make([]string, 0)
	for i := range records {
		email := strings.TrimSpace(records[i][column])
		if email == "" {
			continue
		}
		emails = append(emails, email)
	}
	return emails, nil
}

// LoadContributors satisfies the ContributorFetcher interface. In
// particular, it fetches the provided sheetURL in NewSpreadsheetFetcher,
// then searches for the first column with a first row that contains
// SpreadsheetFetcher.ColumnName. All subsequent rows in that column are
// returned.
func (s *SpreadsheetFetcher) LoadContributors(ctx context.Context) ([]string, error) {
	body, err := downloadCSV(ctx, s.sheetURL)
	if err != nil {
		return nil, err
	}
	columnName := s.ColumnName
	if columnName == "" {
		columnName = "GitHub Username"
	}
	usernames, err := getUsernames(body, s.ColumnName)
	if err != nil {
		return nil, err
	}
	return usernames, nil
}
