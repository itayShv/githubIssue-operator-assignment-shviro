package utils

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
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
