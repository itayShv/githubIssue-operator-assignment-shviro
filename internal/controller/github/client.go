package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	gogithub "github.com/google/go-github/v90/github"

	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
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
			return nil, err
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
		return nil, err
	}
	return issue, nil
}

// GetIssue returns the issue with the given number.
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*Issue, error) {
	issue, _, err := c.gh.Issues.Get(ctx, owner, repo, number)
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// UpdateIssue sets the issue title and description and returns the updated issue.
func (c *Client) UpdateIssue(ctx context.Context, owner, repo string, number int, title, body string) (*Issue, error) {
	issue, _, err := c.gh.Issues.Update(ctx, owner, repo, number, gogithub.UpdateIssueRequest{Title: &title, Body: &body})
	if err != nil {
		return nil, err
	}
	return issue, nil
}

// CloseIssue sets the issue state to closed.
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	_, _, err := c.gh.Issues.Update(ctx, owner, repo, number,
		gogithub.UpdateIssueRequest{State: gogithub.Ptr(utils.GitHubIssueStateClosed)})
	return err
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
			return false, err
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

// StatusCode returns the HTTP status GitHub answered a failed call with, or 0 if err isn't an answer from
// GitHub (or is nil). For an issue, 404 means not found, 410 deleted and 301 moved; for a repository,
// 410 means issues are disabled.
func StatusCode(err error) int {
	var redirect *gogithub.RedirectionError
	if errors.As(err, &redirect) {
		return redirect.StatusCode
	}
	var errResp *gogithub.ErrorResponse
	if errors.As(err, &errResp) && errResp.Response != nil {
		return errResp.Response.StatusCode
	}
	return 0
}
