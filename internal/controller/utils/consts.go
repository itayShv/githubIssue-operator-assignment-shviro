// Package utils holds the constants and helpers shared by the controller and its finalizer helpers.
package utils

import "time"

// Names this operator sets on GithubIssue CRs.
const (
	// GitHubIssueDeletionFinalizer makes Kubernetes wait until the operator has closed the issue.
	GitHubIssueDeletionFinalizer = "github.itayshviro.dev/finalizer"
	// IssueNumberAnnotation holds the number of the GitHub issue a GithubIssue CR manages.
	IssueNumberAnnotation = "github.itayshviro.dev/issue-number"
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
