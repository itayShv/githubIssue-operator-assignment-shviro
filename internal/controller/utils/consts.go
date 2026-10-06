// Package utils holds the constants and helpers shared by the controller and the GitHub client.
package utils

import "time"

// Names this operator sets on GithubIssue CRs.
const (
	// GitHubIssueDeletionFinalizer makes Kubernetes wait until the operator has closed the issue.
	GitHubIssueDeletionFinalizer = "github.itayshviro.dev/finalizer"
	// IssueNumberAnnotation holds the number of the GitHub issue a GithubIssue CR manages.
	IssueNumberAnnotation = "github.itayshviro.dev/issue-number"
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

// ResyncPeriod is how often every GithubIssue is reconciled against GitHub.
const ResyncPeriod = time.Minute

// Status condition types.
const (
	// ConditionReady tells whether the CR is in sync with its GitHub issue.
	ConditionReady = "Ready"
	// ConditionIssueOpen tells whether the GitHub issue is open. It is absent when the CR manages no issue.
	ConditionIssueOpen = "IssueOpen"
	// ConditionIssueHasPR tells whether a pull request is linked to the GitHub issue.
	// It is absent when the CR manages no issue.
	ConditionIssueHasPR = "IssueHasPR"
)

// Reasons for the Ready condition.
const (
	ReasonSynced          = "Synced"
	ReasonDuplicateIssue  = "DuplicateIssue"
	ReasonIssueDeleted    = "IssueDeleted"
	ReasonIssueNotFound   = "IssueNotFound"
	ReasonReconcileFailed = "ReconcileFailed"
)

// Reasons for the IssueOpen condition. Deleted and NotFound are used for IssueHasPR too.
const (
	ReasonOpen     = "Open"
	ReasonClosed   = "Closed"
	ReasonDeleted  = "Deleted"
	ReasonNotFound = "NotFound"
)

// Reasons for the IssueHasPR condition.
const (
	ReasonPullRequestLinked = "PullRequestLinked"
	ReasonNoPullRequest     = "NoPullRequest"
)
