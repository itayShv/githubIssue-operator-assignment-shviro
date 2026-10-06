package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

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

// IssueResponse is the subset of the GitHub issue fields the operator uses.
type IssueResponse struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	Body        string    `json:"body"`
	State       string    `json:"state"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
}

// timelineEvent is the part of an issue timeline event the operator reads.
type timelineEvent struct {
	Event string `json:"event"`
}

// issueRequest is the body for creating or updating an issue.
// Body is a pointer so an empty description is still sent, while nil leaves it out.
type issueRequest struct {
	Title string  `json:"title,omitempty"`
	Body  *string `json:"body,omitempty"`
	State string  `json:"state,omitempty"`
}

// Client is a minimal GitHub REST client for issues.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient returns a Client for the given token. An empty baseURL means api.github.com.
func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = utils.GitHubAPIBaseURL
	}
	return &Client{
		BaseURL: strings.TrimSuffix(baseURL, "/"),
		Token:   token,
		HTTPClient: &http.Client{
			Timeout: utils.GitHubHTTPTimeout,
			// Don't follow redirects: Go turns a redirected PATCH into a GET, so updates and closes would
			// silently do nothing. A moved issue or repository is returned as ErrMoved instead.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

// FindIssueByTitle returns the open issue whose title exactly matches, or nil if none does.
// Pull requests (the issues API returns them too) and issues whose numbers are in skip are ignored.
func (c *Client) FindIssueByTitle(ctx context.Context, owner, repo, title string, skip map[int]bool) (*IssueResponse, error) {
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=%d&page=%d",
			url.PathEscape(owner), url.PathEscape(repo), utils.GitHubPageSize, page)
		var issues []IssueResponse
		if err := c.do(ctx, http.MethodGet, path, nil, &issues); err != nil {
			return nil, repoError(owner, repo, err)
		}
		if len(issues) == 0 {
			return nil, nil
		}
		for i := range issues {
			//return only issues, not pull requests
			if issues[i].PullRequest == nil && issues[i].Title == title && !skip[issues[i].Number] {
				return &issues[i], nil
			}
		}
	}
}

// CreateIssue opens a new issue and returns it.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string) (*IssueResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues", url.PathEscape(owner), url.PathEscape(repo))
	var issue IssueResponse
	if err := c.do(ctx, http.MethodPost, path, issueRequest{Title: title, Body: &body}, &issue); err != nil {
		return nil, repoError(owner, repo, err)
	}
	return &issue, nil
}

// GetIssue returns the issue with the given number.
func (c *Client) GetIssue(ctx context.Context, owner, repo string, number int) (*IssueResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	var issue IssueResponse
	if err := c.do(ctx, http.MethodGet, path, nil, &issue); err != nil {
		return nil, issueError(owner, repo, number, err)
	}
	return &issue, nil
}

// UpdateIssue sets the issue title and description and returns the updated issue.
func (c *Client) UpdateIssue(ctx context.Context, owner, repo string, number int, title, body string) (*IssueResponse, error) {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	var issue IssueResponse
	if err := c.do(ctx, http.MethodPatch, path, issueRequest{Title: title, Body: &body}, &issue); err != nil {
		return nil, issueError(owner, repo, number, err)
	}
	return &issue, nil
}

// CloseIssue sets the issue state to closed.
func (c *Client) CloseIssue(ctx context.Context, owner, repo string, number int) error {
	path := fmt.Sprintf("/repos/%s/%s/issues/%d", url.PathEscape(owner), url.PathEscape(repo), number)
	err := c.do(ctx, http.MethodPatch, path, issueRequest{State: utils.GitHubIssueStateClosed}, nil)
	return issueError(owner, repo, number, err)
}

// HasLinkedPullRequest reports whether a pull request is linked to the issue from its "Development" sidebar.
// It counts the issue timeline's "connected" events minus its "disconnected" events. The REST API doesn't
// say which pull request was linked or what state it is in.
func (c *Client) HasLinkedPullRequest(ctx context.Context, owner, repo string, number int) (bool, error) {
	linked := 0
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/timeline?per_page=%d&page=%d",
			url.PathEscape(owner), url.PathEscape(repo), number, utils.GitHubPageSize, page)
		var events []timelineEvent
		if err := c.do(ctx, http.MethodGet, path, nil, &events); err != nil {
			return false, issueError(owner, repo, number, err)
		}
		for _, e := range events {
			switch e.Event {
			case utils.GitHubEventConnected:
				linked++
			case utils.GitHubEventDisconnected:
				linked--
			}
		}
		// A short page is the last one
		if len(events) < utils.GitHubPageSize {
			return linked > 0, nil
		}
	}
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

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	//executes the request - sends the http request to the github api
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", utils.GitHubMediaType)
	req.Header.Set(utils.GitHubAPIVersionHeader, utils.GitHubAPIVersion)
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	if body != nil {
		req.Header.Set("Content-Type", utils.JSONContentType)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	switch resp.StatusCode {
	case http.StatusMovedPermanently:
		return ErrMoved
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusGone:
		return ErrGone
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("github API %s %s returned %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}
