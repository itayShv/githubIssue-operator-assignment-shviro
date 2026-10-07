package utils

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
)

const (
	repoURL      = "https://github.com/test-owner/test-repo"
	otherRepoURL = "https://github.com/test-owner/other-repo"
	testTitle    = "Login bug"
)

func TestParseRepoURL(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		expectedOwner string
		expectedRepo  string
		expectedErr   bool
	}{
		{name: "plain", url: "https://github.com/itay/foo", expectedOwner: "itay", expectedRepo: "foo", expectedErr: false},
		{name: "with a trailing slash", url: "https://github.com/dana-team/bar/", expectedOwner: "dana-team", expectedRepo: "bar", expectedErr: false},
		{name: "owner only", url: "https://github.com/itay", expectedOwner: "", expectedRepo: "", expectedErr: true},
		{name: "a page inside the repository", url: "https://github.com/itay/foo/issues", expectedOwner: "", expectedRepo: "", expectedErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			owner, repo, err := ParseRepoURL(tt.url)
			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			assert.Equal(t, tt.expectedOwner, owner)
			assert.Equal(t, tt.expectedRepo, repo)
		})
	}
}

func TestSameRepo(t *testing.T) {
	tests := []struct {
		name     string
		a        string
		b        string
		expected bool
	}{
		{name: "identical", a: otherRepoURL, b: otherRepoURL, expected: true},
		{name: "different letter case and a trailing slash", a: repoURL, b: "https://github.com/Test-Owner/Test-Repo/", expected: true},
		{name: "another repository", a: repoURL, b: otherRepoURL, expected: false},
		{name: "an invalid URL", a: repoURL, b: "https://github.com/test-owner", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, SameRepo(tt.a, tt.b))
		})
	}
}

func TestIsOlder(t *testing.T) {
	tests := []struct {
		name     string
		a        *githubv1alpha1.GithubIssue
		b        *githubv1alpha1.GithubIssue
		expected bool
	}{
		{
			name:     "earlier creation time, even with a later name",
			a:        newCR("b", repoURL, testTitle, 0, ""),
			b:        newCR("a", repoURL, testTitle, 1, ""),
			expected: true,
		},
		{
			name:     "later creation time, even with an earlier name",
			a:        newCR("a", repoURL, testTitle, 1, ""),
			b:        newCR("b", repoURL, testTitle, 0, ""),
			expected: false,
		},
		{
			name:     "same creation time, earlier namespace/name",
			a:        newCR("b", repoURL, testTitle, 0, ""),
			b:        newCR("c", repoURL, testTitle, 0, ""),
			expected: true,
		},
		{
			name:     "same creation time, later namespace/name",
			a:        newCR("c", repoURL, testTitle, 0, ""),
			b:        newCR("b", repoURL, testTitle, 0, ""),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, IsOlder(tt.a, tt.b))
		})
	}
}

func TestManagedIssueNumber(t *testing.T) {
	tests := []struct {
		name           string
		cr             *githubv1alpha1.GithubIssue
		others         []*githubv1alpha1.GithubIssue
		expectedNumber int
		expectedOK     bool
	}{
		{name: "no annotation", cr: newCR("cr", repoURL, testTitle, 0, ""), expectedNumber: 0, expectedOK: false},
		{name: "annotation isn't a number", cr: newCR("cr", repoURL, testTitle, 0, "abc"), expectedNumber: 0, expectedOK: false},
		{name: "annotation is zero", cr: newCR("cr", repoURL, testTitle, 0, "0"), expectedNumber: 0, expectedOK: false},
		{name: "annotation is negative", cr: newCR("cr", repoURL, testTitle, 0, "-3"), expectedNumber: 0, expectedOK: false},
		{
			name: "manages the issue when only newer CRs and other repos have the number",
			cr:   newCR("cr", repoURL, testTitle, 1, "42"),
			others: []*githubv1alpha1.GithubIssue{
				newCR("newer-copy", repoURL, testTitle, 2, "42"),
				newCR("older-other-repo", otherRepoURL, testTitle, 0, "42"),
			},
			expectedNumber: 42,
			expectedOK:     true,
		},
		{
			name:           "annotation is a copy when an older CR has the same repo and number",
			cr:             newCR("cr", repoURL, testTitle, 1, "42"),
			others:         []*githubv1alpha1.GithubIssue{newCR("original", repoURL, testTitle, 0, "42")},
			expectedNumber: 0,
			expectedOK:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			number, ok, err := ManagedIssueNumber(t.Context(), readerWith(tt.cr, tt.others...), tt.cr)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedNumber, number)
			assert.Equal(t, tt.expectedOK, ok)
		})
	}
}

func TestCheckTitleClaim(t *testing.T) {
	tests := []struct {
		name            string
		cr              *githubv1alpha1.GithubIssue
		others          []*githubv1alpha1.GithubIssue
		expectedBlocker string
		expectedClaimed map[int]bool
	}{
		{
			name: "no blocker when the others are newer and unlinked, or have another title or repo",
			cr:   newCR("cr", repoURL, testTitle, 1, ""),
			others: []*githubv1alpha1.GithubIssue{
				newCR("newer-same-title", repoURL, testTitle, 2, ""),
				newCR("other-title", repoURL, "Other", 0, "7"),
				newCR("other-repo", otherRepoURL, testTitle, 0, "8"),
			},
			expectedBlocker: "",
			expectedClaimed: map[int]bool{7: true},
		},
		{
			name:            "a linked CR with the same title blocks, even a newer one",
			cr:              newCR("cr", repoURL, testTitle, 0, ""),
			others:          []*githubv1alpha1.GithubIssue{newCR("linked", repoURL, testTitle, 1, "42")},
			expectedBlocker: "default/linked",
			expectedClaimed: map[int]bool{42: true},
		},
		{
			name:            "an older CR with the same title blocks, even if it isn't linked yet",
			cr:              newCR("cr", repoURL, testTitle, 1, ""),
			others:          []*githubv1alpha1.GithubIssue{newCR("older", repoURL, testTitle, 0, "")},
			expectedBlocker: "default/older",
			expectedClaimed: map[int]bool{},
		},
		{
			name: "the oldest blocker wins, in any namespace",
			cr:   newCR("cr", repoURL, testTitle, 2, ""),
			others: []*githubv1alpha1.GithubIssue{
				newCR("linked", repoURL, testTitle, 1, "42"),
				func() *githubv1alpha1.GithubIssue {
					cr := newCR("oldest", repoURL, testTitle, 0, "")
					cr.Namespace = "team2"
					return cr
				}(),
			},
			expectedBlocker: "team2/oldest",
			expectedClaimed: map[int]bool{42: true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			blocker, claimed, err := CheckTitleClaim(t.Context(), readerWith(tt.cr, tt.others...), tt.cr)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedBlocker, namespacedName(blocker))
			assert.Equal(t, tt.expectedClaimed, claimed)
		})
	}
}

// start is the creation time of the oldest CR in these tests.
var start = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var testScheme = func() *runtime.Scheme {
	s := runtime.NewScheme()
	utilruntime.Must(githubv1alpha1.AddToScheme(s))
	return s
}()

// newCR returns a CR in namespace default, created the given number of minutes after start. A non-empty
// annotation is set, as is, as the CR's issue number annotation. The fake client doesn't assign UIDs, so
// each CR gets its name as UID: the code under test skips the CR itself by comparing UIDs.
func newCR(name, repo, title string, minute int, annotation string) *githubv1alpha1.GithubIssue {
	cr := &githubv1alpha1.GithubIssue{
		ObjectMeta: metav1.ObjectMeta{
			Name:              name,
			Namespace:         "default",
			UID:               types.UID(name),
			CreationTimestamp: metav1.NewTime(start.Add(time.Duration(minute) * time.Minute)),
		},
		Spec: githubv1alpha1.GithubIssueSpec{Repo: repo, Title: title},
	}
	if annotation != "" {
		cr.Annotations = map[string]string{IssueNumberAnnotation: annotation}
	}
	return cr
}

// readerWith returns a reader that holds cr and the other CRs, like the controller's cache or API reader.
func readerWith(cr *githubv1alpha1.GithubIssue, others ...*githubv1alpha1.GithubIssue) client.Reader {
	objs := make([]client.Object, 0, 1+len(others))
	objs = append(objs, cr)
	for _, other := range others {
		objs = append(objs, other)
	}
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).Build()
}

// namespacedName returns the namespace/name of cr, or "" for nil.
func namespacedName(cr *githubv1alpha1.GithubIssue) string {
	if cr == nil {
		return ""
	}
	return client.ObjectKeyFromObject(cr).String()
}
