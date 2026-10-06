package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gogithub "github.com/google/go-github/v90/github"

	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

var (
	// ErrNotFound is returned (wrapped with the repo or issue details) when GitHub answers 404:
	// the repo or issue doesn't exist, or the token can't see it.
	ErrNotFound = errors.New("not found")
	// ErrGone is returned (wrapped with the repo or issue details) when GitHub answers 410:
	// the issue was deleted, or issues are disabled in the repository.
	ErrGone = errors.New("gone (deleted, or issues are disabled in the repository)")
	// ErrMoved is returned (wrapped with the repo or issue details) when GitHub answers 301:
	// the issue was transferred to another repository, or the repository was renamed or transferred.
	ErrMoved = errors.New("moved (the issue was transferred, or the repository was renamed or transferred)")
)

// Issue is a GitHub issue as go-github returns it. Read its fields with the getters (GetTitle, GetNumber...).
type Issue = gogithub.Issue

// Client is the operator's GitHub issues client, on top of go-github.
type Client struct {
	gh *gogithub.Client
}

// NewClient returns a Client for the given token. An empty baseURL means api.github.com.
func NewClient(baseURL, token string) (*Client, error) {
	opts := []gogithub.ClientOptionsFunc{
		gogithub.WithHTTPClient(&http.Client{
			Timeout: utils.GitHubHTTPTimeout,
			// Don't follow redirects: Go turns a redirected PATCH into a GET, so updates and closes would
			// silently do nothing. go-github then returns a moved issue or repository as a RedirectionError.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}),
	}
	if token != "" {
		opts = append(opts, gogithub.WithAuthToken(token))
	}
	if baseURL != "" {
		opts = append(opts, gogithub.WithURLs(&baseURL, nil))
	}
	gh, err := gogithub.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("creating GitHub client: %w", err)
	}
	return &Client{gh: gh}, nil
}

// FindIssueByTitle returns the open issue whose title exactly matches, or nil if none does.
// Pull requests (the issues API returns them too) and issues whose numbers are in skip are ignored.
func (c *Client) FindIssueByTitle(ctx context.Context, owner, repo, title string, skip map[int]bool) (*Issue, error) {
	opts := &gogithub.IssueListByRepoOptions{
		State:       "open",
		ListOptions: gogithub.ListOptions{PerPage: utils.GitHubPageSize},
	}
	for {
		issues, resp, err := c.gh.Issues.ListByRepo(ctx, owner, repo, opts)
		if err != nil {
			return nil, repoError(owner, repo, mapError(err))
		}
		for _, issue := range issues {
			// return only issues, not pull requests
			if !issue.IsPullRequest() && issue.GetTitle() == title && !skip[issue.GetNumber()] {
				return issue, nil
			}
		}
		if resp.NextPage == 0 {
			return nil, nil
		}
		opts.ListOptions.Page = resp.NextPage
	}
}

// CreateIssue opens a new issue and returns it.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string) (*Issue, error) {
	issue, _, err := c.gh.Issues.Create(ctx, owner, repo, gogithub.CreateIssueRequest{Title: title, Body: &body})
	if err != nil {
		return nil, repoError(owner, repo, mapError(err))
	}
	return issue, nil
}

// GetIssue returns the issue with the given number.
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	issue, _, err := c.gh.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, issueError(owner, repo, number, mapError(err))
	}
	return issue, nil
}

// UpdateIssue sets the issue title and description and returns the updated issue.
func (c *Client) UpdateIssue(ctx context.Context, owner, repo string, number int, title, body string) (*Issue, error) {
	issue, _, err := c.gh.Issues.Update(ctx, owner, repo, number, gogithub.UpdateIssueRequest{Title: &title, Body: &body})
	if err != nil {
		return nil, issueError(owner, repo, number, mapError(err))
	}
	return issue, nil
}

// CloseIssue sets the issue state to closed.
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	_, _, err := c.gh.Issues.Update(ctx, owner, repo, number,
		gogithub.UpdateIssueRequest{State: gogithub.Ptr(utils.GitHubIssueStateClosed)})
	return issueError(owner, repo, number, mapError(err))
}

// HasLinkedPullRequest reports whether a pull request is linked to the issue from its "Development" sidebar.
// It counts the issue timeline's "connected" events minus its "disconnected" events. The REST API doesn't
// say which pull request was linked or what state it is in.
func (c *Client) HasLinkedPullRequest(ctx context.Context, owner, repo string, number int) (bool, error) {
	linked := 0
	opts := &gogithub.ListOptions{PerPage: utils.GitHubPageSize}
	for {
		events, resp, err := c.gh.Issues.ListIssueTimeline(ctx, owner, repo, number, opts)
		if err != nil {
			return false, issueError(owner, repo, number, mapError(err))
		}
		for _, e := range events {
			switch e.GetEvent() {
			case utils.GitHubEventConnected:
				linked++
			case utils.GitHubEventDisconnected:
				linked--
			}
		}
		if resp.NextPage == 0 {
			return linked > 0, nil
		}
		opts.Page = resp.NextPage
	}
}

// mapError turns go-github's errors for 404, 410 and 301 into ErrNotFound, ErrGone and ErrMoved,
// leaving other errors unchanged.
func mapError(err error) error {
	var redirect *gogithub.RedirectionError
	if errors.As(err, &redirect) && redirect.StatusCode == http.StatusMovedPermanently {
		return ErrMoved
	}
	var errResp *gogithub.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		switch errResp.Response.StatusCode {
		case http.StatusNotFound:
			return ErrNotFound
		case http.StatusGone:
			return ErrGone
		}
	}
	return err
}

// repoError adds the repository to a not-found, gone or moved error, leaving other errors unchanged.
func repoError(owner, repo string, err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrGone) || errors.Is(err, ErrMoved) {
		return fmt.Errorf("github repository %s/%s: %w", owner, repo, err)
	}
	return err
}

// issueError adds the issue to a not-found, gone or moved error, leaving other errors unchanged.
func issueError(owner, repo string, number int, err error) error {
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrGone) || errors.Is(err, ErrMoved) {
		return fmt.Errorf("github issue %s/%s#%d: %w", owner, repo, number, err)
	}
	return err
}
