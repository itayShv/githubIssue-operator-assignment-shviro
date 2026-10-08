package github

import (
	"errors"
	"time"
)

// GitHub REST API settings.
const (
	GitHubAPIBaseURL = "https://api.github.com"
	// GitHubAPIVersion pins the REST API version so GitHub can't change response shapes under us.
	// See https://docs.github.com/en/rest/about-the-rest-api/api-versions
	GitHubAPIVersion       = "2026-03-10"
	GitHubAPIVersionHeader = "X-GitHub-Api-Version"
	GitHubMediaType        = "application/vnd.github+json"
	JSONContentType        = "application/json"
	GitHubHTTPTimeout      = 30 * time.Second
	// GitHubPageSize is how many items to request per page from list endpoints (GitHub's maximum).
	GitHubPageSize = 100
)

// Values in GitHub's responses that the operator reads.
const (
	GitHubIssueStateClosed = "closed"
	// Issue timeline events for linking and unlinking a pull request from the issue's "Development" sidebar.
	GitHubEventConnected    = "connected"
	GitHubEventDisconnected = "disconnected"
)

// Errors the GitHub client returns for GitHub's 404, 410 and 301 answers. Check them with errors.Is.
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
