package utils

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
	"github.com/itayshviro/githubissue-operator/internal/controller/github"
)

// ParseRepoURL extracts owner and repo from a URL like https://github.com/owner/repo.
func ParseRepoURL(repoURL string) (owner, repo string, err error) {
	u, err := url.Parse(repoURL)
	if err != nil {
		return "", "", fmt.Errorf("invalid repo URL %q: %w", repoURL, err)
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo URL %q: expected https://github.com/<owner>/<repo>", repoURL)
	}
	return parts[0], parts[1], nil
}

// SameRepo reports whether two repo URLs point to the same GitHub repository.
// GitHub owner and repo names are case-insensitive, and a trailing slash doesn't matter.
func SameRepo(a, b string) bool {
	aOwner, aRepo, err := ParseRepoURL(a)
	if err != nil {
		return false
	}
	bOwner, bRepo, err := ParseRepoURL(b)
	if err != nil {
		return false
	}
	return strings.EqualFold(aOwner, bOwner) && strings.EqualFold(aRepo, bRepo)
}

// ManagedIssueNumber returns the number of the GitHub issue the CR manages. ok is false when it manages none:
// the annotation is missing or invalid, or an older CR has the same repo and number (the annotation was copied).
// Other CRs are listed across all namespaces, since an issue is global.
func ManagedIssueNumber(ctx context.Context, reader client.Reader, cr *githubv1alpha1.GithubIssue) (number int, ok bool, err error) {
	value, found := cr.Annotations[IssueNumberAnnotation]
	if !found {
		return 0, false, nil
	}
	number, err = strconv.Atoi(value)
	if err != nil || number <= 0 {
		logf.FromContext(ctx).Info("Ignoring invalid issue number annotation", "name", cr.Name, "value", value)
		return 0, false, nil
	}

	var all githubv1alpha1.GithubIssueList
	if err := reader.List(ctx, &all); err != nil {
		return 0, false, fmt.Errorf("failed to list GithubIssues: %w", err)
	}
	for i := range all.Items {
		other := &all.Items[i]
		if other.UID == cr.UID {
			continue
		}
		otherNumber, err := strconv.Atoi(other.Annotations[IssueNumberAnnotation])
		if err != nil || otherNumber != number || !SameRepo(cr.Spec.Repo, other.Spec.Repo) {
			continue
		}
		if IsOlder(other, cr) {
			return 0, false, nil
		}
	}
	return number, true, nil
}

// CheckTitleClaim is used before a CR that isn't linked to an issue links itself to one by title.
// It lists CRs cluster-wide and returns:
//   - blocker: another CR with the same repo and title that comes first, either because it is already
//     linked to an issue or because it is older. nil means this CR may link.
//   - claimed: numbers of the repo's issues that are already linked to other CRs, which must not be adopted.
func CheckTitleClaim(ctx context.Context, reader client.Reader, cr *githubv1alpha1.GithubIssue) (blocker *githubv1alpha1.GithubIssue, claimed map[int]bool, err error) {
	var all githubv1alpha1.GithubIssueList
	if err := reader.List(ctx, &all); err != nil {
		return nil, nil, fmt.Errorf("failed to list GithubIssues: %w", err)
	}
	claimed = map[int]bool{}
	for i := range all.Items {
		other := &all.Items[i]
		if other.UID == cr.UID || !SameRepo(cr.Spec.Repo, other.Spec.Repo) {
			continue
		}
		number, err := strconv.Atoi(other.Annotations[IssueNumberAnnotation])
		linked := err == nil && number > 0
		if linked {
			//another cr already mannages this issue - prevents more than one cr on the same issue
			claimed[number] = true
		}
		if other.Spec.Title == cr.Spec.Title && (linked || IsOlder(other, cr)) {
			if blocker == nil || IsOlder(other, blocker) {
				//prevents creation of duplicate issue , duplicates will wait for their turn by creation date
				blocker = other
			}
		}
	}
	return blocker, claimed, nil
}

// IsOlder reports whether a was created before b, using namespace/name as a tie-break.
func IsOlder(a, b *githubv1alpha1.GithubIssue) bool {
	if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
		return a.CreationTimestamp.Before(&b.CreationTimestamp)
	}
	return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
}

// CloseManagedIssue closes the GitHub issue the CR manages, if it manages one. An issue that GitHub can't
// find, or that was deleted or moved, is left as it is.
func CloseManagedIssue(ctx context.Context, c client.Client, gh *github.Client, cr *githubv1alpha1.GithubIssue) error {
	logger := logf.FromContext(ctx)

	number, ok, err := ManagedIssueNumber(ctx, c, cr)
	if err != nil {
		return err
	}
	if !ok {
		logger.Info("GithubIssue does not manage a GitHub issue, skipping issue close", "name", cr.Name)
		return nil
	}
	owner, repo, err := ParseRepoURL(cr.Spec.Repo)
	if err != nil {
		return err
	}
	err = gh.CloseIssue(ctx, owner, repo, number)
	switch {
	case errors.Is(err, github.ErrNotFound), errors.Is(err, github.ErrGone), errors.Is(err, github.ErrMoved):
		logger.Info("GitHub issue was not found, deleted or moved, nothing to close", "error", err.Error())
	case err != nil:
		logger.Error(err, "Failed to close GitHub issue", "number", number)
		return err
	default:
		logger.Info("Closed GitHub issue", "number", number)
	}
	return nil
}

// SyncIssue makes the GitHub issue match the CR, linking the CR to an issue first if needed, and writes the
// CR's status. reader should read straight from the API server: it is used to check that no other CR has
// claimed the issue a moment earlier.
func SyncIssue(ctx context.Context, c client.Client, reader client.Reader, gh *github.Client, cr *githubv1alpha1.GithubIssue) error {
	logger := logf.FromContext(ctx)
	// handle create or edit event
	logger.Info("Starting Update procedure for GithubIssue", "name", cr.Name, "repo", cr.Spec.Repo, "title", cr.Spec.Title)
	defer logger.Info("Finish Update procedure for GithubIssue", "name", cr.Name, "repo", cr.Spec.Repo, "title", cr.Spec.Title)

	s := &issueSync{kube: c, reader: reader, gh: gh, cr: cr}
	owner, repo, err := ParseRepoURL(cr.Spec.Repo)
	if err != nil {
		return s.fail(ctx, err)
	}
	s.owner, s.repo = owner, repo

	issue, err := s.findIssue(ctx)
	if err != nil || issue == nil {
		// No issue to sync: the status already says why
		return err
	}
	return s.syncIssue(ctx, issue)
}

// issueSync holds what the steps of SyncIssue need: the clients, and the CR being synced with its repository.
type issueSync struct {
	// kube is the controller's client. It reads the CRs and updates the CR and its status.
	kube client.Client
	// reader reads straight from the API server, for the check that no other CR claims the issue.
	reader client.Reader
	gh     *github.Client
	cr     *githubv1alpha1.GithubIssue
	owner  string
	repo   string
}

// findIssue returns the issue the CR manages, linking one by title if the CR isn't linked yet. When there is
// nothing to sync (the issue is gone on GitHub, or another CR comes first), it writes the status that says
// why and returns a nil issue.
func (s *issueSync) findIssue(ctx context.Context) (*github.IssueResponse, error) {
	// Find the issue this CR manages
	number, linked, err := ManagedIssueNumber(ctx, s.kube, s.cr)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if linked {
		return s.fetchManagedIssue(ctx, number)
	}

	// A copied or invalid annotation doesn't count: remove it, so the CR links by title like a new one
	if err := s.removeIssueAnnotation(ctx); err != nil {
		return nil, s.fail(ctx, err)
	}
	// if issue dont exist -> find an open one with this title or create it, unless another CR comes first
	issue, blocker, err := s.linkIssue(ctx)
	if err != nil {
		return nil, s.fail(ctx, err)
	}
	if blocker != nil {
		logf.FromContext(ctx).Info("GithubIssue is a duplicate, not managing a GitHub issue", "name", s.cr.Name, "managedBy", blocker.Namespace+"/"+blocker.Name)
		return nil, s.setDuplicateStatus(ctx, blocker)
	}
	return issue, nil
}

// fetchManagedIssue fetches the issue the CR is linked to. When GitHub answers that the issue was deleted or
// moved, or can't find it, it reports that in the status and returns a nil issue.
func (s *issueSync) fetchManagedIssue(ctx context.Context, number int) (*github.IssueResponse, error) {
	logger := logf.FromContext(ctx)
	issue, err := s.gh.GetIssue(ctx, s.owner, s.repo, number)
	switch {
	case err == nil:
		return issue, nil
	case errors.Is(err, github.ErrGone), errors.Is(err, github.ErrMoved):
		// Keep the annotation, so the CR keeps its claim on the title and no other CR recreates the issue.
		// A moved issue is treated as deleted, since the CR tracks its issue by repo and number.
		logger.Info("GitHub issue was deleted or moved", "number", number, "error", err.Error())
		return nil, s.setIssueMissingStatus(ctx, number, err)
	case errors.Is(err, github.ErrNotFound):
		// Often a token or access problem, so keep the annotation and report it
		logger.Info("GitHub issue was not found", "number", number, "error", err.Error())
		return nil, s.setIssueMissingStatus(ctx, number, err)
	default:
		return nil, s.fail(ctx, err)
	}
}

// syncIssue updates the issue's title and description if they differ from the CR, checks whether a pull
// request is linked to it, and reports the issue in the status.
func (s *issueSync) syncIssue(ctx context.Context, issue *github.IssueResponse) error {
	// if the CR or the issue changed, patch the github issue so it matches the CR
	if issue.Title != s.cr.Spec.Title || issue.Body != s.cr.Spec.Description {
		updated, err := s.gh.UpdateIssue(ctx, s.owner, s.repo, issue.Number, s.cr.Spec.Title, s.cr.Spec.Description)
		if err != nil {
			return s.fail(ctx, err)
		}
		issue = updated
		logf.FromContext(ctx).Info("Updated GitHub issue", "number", issue.Number)
	}

	// issue has a PR
	hasPR, err := s.gh.HasLinkedPullRequest(ctx, s.owner, s.repo, issue.Number)
	if err != nil {
		return s.fail(ctx, err)
	}
	return s.setSyncedStatus(ctx, issue, hasPR)
}

// removeIssueAnnotation removes the issue number annotation from the CR, if it has one.
func (s *issueSync) removeIssueAnnotation(ctx context.Context) error {
	if _, found := s.cr.Annotations[IssueNumberAnnotation]; !found {
		return nil
	}
	delete(s.cr.Annotations, IssueNumberAnnotation)
	if err := s.kube.Update(ctx, s.cr); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Removed issue number annotation from GithubIssue", "name", s.cr.Name)
	return nil
}

// linkIssue finds an open issue with the CR's title, or creates one, and saves its number on the CR.
// If another CR comes first for this repo and title, it returns that CR instead and changes nothing.
func (s *issueSync) linkIssue(ctx context.Context) (*github.IssueResponse, *githubv1alpha1.GithubIssue, error) {
	logger := logf.FromContext(ctx)

	blocker, claimed, err := CheckTitleClaim(ctx, s.reader, s.cr)
	if err != nil {
		return nil, nil, err
	}
	if blocker != nil {
		return nil, blocker, nil
	}

	// Skip issues other CRs already manage(a CR uses its issue number), e.g. one whose CR was just renamed but not yet reconciled
	issue, err := s.gh.FindIssueByTitle(ctx, s.owner, s.repo, s.cr.Spec.Title, claimed)
	if err != nil {
		return nil, nil, err
	}
	if issue == nil {
		issue, err = s.gh.CreateIssue(ctx, s.owner, s.repo, s.cr.Spec.Title, s.cr.Spec.Description)
		if err != nil {
			return nil, nil, err
		}
		logger.Info("Created GitHub issue", "number", issue.Number)
	} else {
		logger.Info("Found open GitHub issue with the same title", "number", issue.Number)
	}

	// Save the link right away, before anything else can fail
	metav1.SetMetaDataAnnotation(&s.cr.ObjectMeta, IssueNumberAnnotation, strconv.Itoa(issue.Number))
	if err := s.kube.Update(ctx, s.cr); err != nil {
		return nil, nil, err
	}
	logger.Info("Saved issue number annotation on GithubIssue", "name", s.cr.Name, "number", issue.Number)
	return issue, nil, nil
}

// condition builds a status condition for the CR's current generation.
func condition(cr *githubv1alpha1.GithubIssue, condType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: reason, Message: message, ObservedGeneration: cr.Generation}
}

// pullRequestCondition reports whether a pull request is linked to the issue.
func pullRequestCondition(cr *githubv1alpha1.GithubIssue, linked bool) metav1.Condition {
	if linked {
		return condition(cr, ConditionIssueHasPR, metav1.ConditionTrue, ReasonPullRequestLinked, "A pull request is linked to the issue")
	}
	return condition(cr, ConditionIssueHasPR, metav1.ConditionFalse, ReasonNoPullRequest, "No pull request is linked to the issue")
}

// setSyncedStatus reports a CR in sync with its issue: Ready is True, IssueOpen follows the issue's state,
// and IssueHasPR says whether a pull request is linked.
func (s *issueSync) setSyncedStatus(ctx context.Context, issue *github.IssueResponse, hasPR bool) error {
	ready := condition(s.cr, ConditionReady, metav1.ConditionTrue, ReasonSynced, fmt.Sprintf("Managing GitHub issue #%d", issue.Number))
	open := condition(s.cr, ConditionIssueOpen, metav1.ConditionTrue, ReasonOpen, fmt.Sprintf("GitHub issue #%d is open", issue.Number))
	if issue.State == github.GitHubIssueStateClosed {
		open = condition(s.cr, ConditionIssueOpen, metav1.ConditionFalse, ReasonClosed, fmt.Sprintf("GitHub issue #%d is closed", issue.Number))
	}
	return s.setStatus(ctx, ready, open, pullRequestCondition(s.cr, hasPR))
}

// setDuplicateStatus reports a CR that waits because blocker comes first for its repo and title.
// It manages no issue, so only Ready is set.
func (s *issueSync) setDuplicateStatus(ctx context.Context, blocker *githubv1alpha1.GithubIssue) error {
	msg := fmt.Sprintf("GithubIssue %s/%s comes first for this repo and title", blocker.Namespace, blocker.Name)
	return s.setStatus(ctx, condition(s.cr, ConditionReady, metav1.ConditionFalse, ReasonDuplicateIssue, msg))
}

// setIssueMissingStatus reports a managed issue that GitHub can't return: deleted or moved (410, 301),
// or not found (404).
func (s *issueSync) setIssueMissingStatus(ctx context.Context, number int, err error) error {
	readyReason, reason, open := ReasonIssueDeleted, ReasonDeleted, metav1.ConditionFalse
	msg := fmt.Sprintf("GitHub issue #%d was deleted", number)
	switch {
	case errors.Is(err, github.ErrMoved):
		msg = fmt.Sprintf("GitHub issue #%d was moved (transferred to another repository, "+
			"or its repository was renamed or transferred)", number)
	case errors.Is(err, github.ErrNotFound):
		readyReason, reason, open = ReasonIssueNotFound, ReasonNotFound, metav1.ConditionUnknown
		msg = err.Error()
	}
	return s.setStatus(ctx,
		condition(s.cr, ConditionReady, metav1.ConditionFalse, readyReason, msg),
		condition(s.cr, ConditionIssueOpen, open, reason, msg),
		condition(s.cr, ConditionIssueHasPR, metav1.ConditionUnknown, reason, msg))
}

// setStatus sets the Ready condition and the issue conditions (IssueOpen, IssueHasPR), and saves the status
// if anything changed. With no issue conditions the CR manages no issue, so they are removed.
func (s *issueSync) setStatus(ctx context.Context, ready metav1.Condition, issueConditions ...metav1.Condition) error {
	changed := meta.SetStatusCondition(&s.cr.Status.Conditions, ready)
	if len(issueConditions) == 0 {
		// The CR manages no issue: remove the conditions left from an issue it managed before
		for _, condType := range []string{ConditionIssueOpen, ConditionIssueHasPR} {
			changed = meta.RemoveStatusCondition(&s.cr.Status.Conditions, condType) || changed
		}
	} else {
		for _, c := range issueConditions {
			changed = meta.SetStatusCondition(&s.cr.Status.Conditions, c) || changed
		}
	}

	if changed {
		if err := s.kube.Status().Update(ctx, s.cr); err != nil {
			return err
		}
	}
	return nil
}

// fail records err in the Ready condition and returns it, so the reconcile is retried with backoff.
func (s *issueSync) fail(ctx context.Context, err error) error {
	ready := condition(s.cr, ConditionReady, metav1.ConditionFalse, ReasonReconcileFailed, err.Error())
	if meta.SetStatusCondition(&s.cr.Status.Conditions, ready) {
		if statusErr := s.kube.Status().Update(ctx, s.cr); statusErr != nil {
			logf.FromContext(ctx).Error(statusErr, "Failed to update GithubIssue status", "name", s.cr.Name)
		}
	}
	return err
}
