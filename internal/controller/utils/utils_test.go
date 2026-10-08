package utils

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"sync"
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
	"github.com/itayshviro/githubissue-operator/internal/controller/github"
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

func TestSyncIssue(t *testing.T) {
	tests := []struct {
		name               string
		cr                 *githubv1alpha1.GithubIssue
		github             *fakeGitHub
		expectedErr        string
		expectedAnnotation string
		expectedConditions map[string]string
		expectedIssues     []github.IssueResponse
	}{
		{
			name:               "creates the issue when no open issue has the title",
			cr:                 issueCR("", "Steps to reproduce"),
			github:             &fakeGitHub{},
			expectedErr:        "",
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonSynced, ConditionIssueOpen: ReasonOpen, ConditionIssueHasPR: ReasonNoPullRequest},
			expectedIssues:     []github.IssueResponse{{Number: 1, Title: testTitle, Body: "Steps to reproduce", State: "open"}},
		},
		{
			name:               "fails when GitHub refuses to create the issue",
			cr:                 issueCR("", "Steps to reproduce"),
			github:             &fakeGitHub{failCreate: true},
			expectedErr:        "403",
			expectedAnnotation: "",
			expectedConditions: map[string]string{ConditionReady: ReasonReconcileFailed},
			expectedIssues:     nil,
		},
		{
			name:               "adopts the open issue with the title and updates its description",
			cr:                 issueCR("", "New description"),
			github:             &fakeGitHub{issues: []github.IssueResponse{{Number: 1, Title: testTitle, Body: "Old description", State: "open"}}},
			expectedErr:        "",
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonSynced, ConditionIssueOpen: ReasonOpen, ConditionIssueHasPR: ReasonNoPullRequest},
			expectedIssues:     []github.IssueResponse{{Number: 1, Title: testTitle, Body: "New description", State: "open"}},
		},
		{
			name:               "fails when GitHub refuses to update the issue",
			cr:                 issueCR("", "New description"),
			github:             &fakeGitHub{issues: []github.IssueResponse{{Number: 1, Title: testTitle, Body: "Old description", State: "open"}}, failUpdate: true},
			expectedErr:        "403",
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonReconcileFailed},
			expectedIssues:     []github.IssueResponse{{Number: 1, Title: testTitle, Body: "Old description", State: "open"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := clientWith(tt.cr)
			err := SyncIssue(t.Context(), c, c, tt.github.serve(t), tt.cr)
			if tt.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.expectedErr)
			}
			saved := savedCR(t, c, tt.cr)
			assert.Equal(t, tt.expectedAnnotation, saved.Annotations[IssueNumberAnnotation])
			assert.Equal(t, tt.expectedConditions, conditionReasons(saved))
			assert.Equal(t, tt.expectedIssues, tt.github.issuesNow())
		})
	}
}

func TestSyncIssueNothingToSync(t *testing.T) {
	// The repository has issue #1. None of these cases change it.
	issues := []github.IssueResponse{{Number: 1, Title: testTitle, Body: "", State: "open"}}

	tests := []struct {
		name               string
		cr                 *githubv1alpha1.GithubIssue
		others             []*githubv1alpha1.GithubIssue
		github             *fakeGitHub
		expectedAnnotation string
		expectedConditions map[string]string
	}{
		{
			name:               "reports an issue that was deleted (410), keeping the link",
			cr:                 issueCR("1", ""),
			others:             nil,
			github:             &fakeGitHub{issues: issues, unavailable: map[int]int{1: http.StatusGone}},
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonIssueDeleted, ConditionIssueOpen: ReasonDeleted, ConditionIssueHasPR: ReasonDeleted},
		},
		{
			name:               "reports an issue that was moved (301), keeping the link",
			cr:                 issueCR("1", ""),
			others:             nil,
			github:             &fakeGitHub{issues: issues, unavailable: map[int]int{1: http.StatusMovedPermanently}},
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonIssueDeleted, ConditionIssueOpen: ReasonDeleted, ConditionIssueHasPR: ReasonDeleted},
		},
		{
			name:               "reports an issue GitHub can't find (404), keeping the link",
			cr:                 issueCR("1", ""),
			others:             nil,
			github:             &fakeGitHub{issues: issues, unavailable: map[int]int{1: http.StatusNotFound}},
			expectedAnnotation: "1",
			expectedConditions: map[string]string{ConditionReady: ReasonIssueNotFound, ConditionIssueOpen: ReasonNotFound, ConditionIssueHasPR: ReasonNotFound},
		},
		{
			name:               "waits as a duplicate when a CR with the same title is linked",
			cr:                 issueCR("", ""),
			others:             []*githubv1alpha1.GithubIssue{newCR("linked", repoURL, testTitle, 0, "1")},
			github:             &fakeGitHub{issues: issues},
			expectedAnnotation: "",
			expectedConditions: map[string]string{ConditionReady: ReasonDuplicateIssue},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := clientWith(tt.cr, tt.others...)
			err := SyncIssue(t.Context(), c, c, tt.github.serve(t), tt.cr)
			require.NoError(t, err)
			saved := savedCR(t, c, tt.cr)
			assert.Equal(t, tt.expectedAnnotation, saved.Annotations[IssueNumberAnnotation])
			assert.Equal(t, tt.expectedConditions, conditionReasons(saved))
			assert.Equal(t, issues, tt.github.issuesNow())
		})
	}
}

func TestCloseManagedIssue(t *testing.T) {
	open := []github.IssueResponse{{Number: 1, Title: testTitle, Body: "", State: "open"}}
	closed := []github.IssueResponse{{Number: 1, Title: testTitle, Body: "", State: github.GitHubIssueStateClosed}}

	tests := []struct {
		name           string
		cr             *githubv1alpha1.GithubIssue
		github         *fakeGitHub
		expectedErr    string
		expectedIssues []github.IssueResponse
	}{
		{name: "closes the issue the CR manages", cr: issueCR("1", ""), github: &fakeGitHub{issues: open}, expectedErr: "", expectedIssues: closed},
		{name: "touches no issue when the CR manages none", cr: issueCR("", ""), github: &fakeGitHub{issues: open}, expectedErr: "", expectedIssues: open},
		{name: "nothing to close when the issue was deleted (410)", cr: issueCR("1", ""), github: &fakeGitHub{issues: open, unavailable: map[int]int{1: http.StatusGone}}, expectedErr: "", expectedIssues: open},
		{name: "nothing to close when the issue was moved (301)", cr: issueCR("1", ""), github: &fakeGitHub{issues: open, unavailable: map[int]int{1: http.StatusMovedPermanently}}, expectedErr: "", expectedIssues: open},
		{name: "nothing to close when GitHub can't find the issue (404)", cr: issueCR("1", ""), github: &fakeGitHub{issues: open, unavailable: map[int]int{1: http.StatusNotFound}}, expectedErr: "", expectedIssues: open},
		{name: "returns any other error", cr: issueCR("1", ""), github: &fakeGitHub{issues: open, failUpdate: true}, expectedErr: "403", expectedIssues: open},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CloseManagedIssue(t.Context(), clientWith(tt.cr), tt.github.serve(t), tt.cr)
			if tt.expectedErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.expectedErr)
			}
			assert.Equal(t, tt.expectedIssues, tt.github.issuesNow())
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

// issueCR returns the CR that the SyncIssue and CloseManagedIssue tests act on: the test title and repository,
// the given issue number annotation ("" for none) and description, created a minute after start.
func issueCR(annotation, description string) *githubv1alpha1.GithubIssue {
	cr := newCR("cr", repoURL, testTitle, 1, annotation)
	cr.Spec.Description = description
	return cr
}

// clientWith returns a fake client that holds cr and the other CRs. Like the API server, it saves the status
// only through the status subresource.
func clientWith(cr *githubv1alpha1.GithubIssue, others ...*githubv1alpha1.GithubIssue) client.Client {
	objs := make([]client.Object, 0, 1+len(others))
	objs = append(objs, cr)
	for _, other := range others {
		objs = append(objs, other)
	}
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).
		WithStatusSubresource(&githubv1alpha1.GithubIssue{}).Build()
}

// readerWith returns a reader that holds cr and the other CRs, like the controller's cache or API reader.
func readerWith(cr *githubv1alpha1.GithubIssue, others ...*githubv1alpha1.GithubIssue) client.Reader {
	return clientWith(cr, others...)
}

// savedCR reads cr back from the client, to check what was saved.
func savedCR(t *testing.T, c client.Client, cr *githubv1alpha1.GithubIssue) *githubv1alpha1.GithubIssue {
	t.Helper()
	var saved githubv1alpha1.GithubIssue
	require.NoError(t, c.Get(t.Context(), client.ObjectKeyFromObject(cr), &saved))
	return &saved
}

// conditionReasons returns the reason of each of the CR's conditions, by condition type.
func conditionReasons(cr *githubv1alpha1.GithubIssue) map[string]string {
	reasons := map[string]string{}
	for _, c := range cr.Status.Conditions {
		reasons[c.Type] = c.Reason
	}
	return reasons
}

// namespacedName returns the namespace/name of cr, or "" for nil.
func namespacedName(cr *githubv1alpha1.GithubIssue) string {
	if cr == nil {
		return ""
	}
	return client.ObjectKeyFromObject(cr).String()
}

// fakeGitHub is an in-memory GitHub repository served by httptest, so the tests never call GitHub. Issue n is
// issues[n-1]. failCreate and failUpdate make creating and updating (or closing) an issue answer 403, as
// GitHub does for a token without write access. unavailable makes GitHub answer a status instead of an issue:
// 410 after a delete, 301 after a transfer, or 404.
type fakeGitHub struct {
	issues      []github.IssueResponse
	failCreate  bool
	failUpdate  bool
	unavailable map[int]int

	mu sync.Mutex
}

// serve starts a server for the repository and returns a GitHub client for it. It copies issues first, so
// table rows can share them. The server stops when the test ends.
func (f *fakeGitHub) serve(t *testing.T) *github.Client {
	t.Helper()
	f.issues = slices.Clone(f.issues)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", f.listIssues)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", f.createIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}", f.getIssue)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", f.updateIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/timeline", f.timeline)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	return github.NewClient(server.URL, "test-token")
}

// issuesNow returns a copy of the repository's issues.
func (f *fakeGitHub) issuesNow() []github.IssueResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.issues)
}

// listIssues answers the list of open issues. They all fit on page 1.
func (f *fakeGitHub) listIssues(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	open := []github.IssueResponse{}
	if r.URL.Query().Get("page") == "1" {
		for _, issue := range f.issues {
			if issue.State == "open" && f.unavailable[issue.Number] == 0 {
				open = append(open, issue)
			}
		}
	}
	writeJSON(w, http.StatusOK, open)
}

func (f *fakeGitHub) createIssue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreate {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
		return
	}
	issue := github.IssueResponse{Number: len(f.issues) + 1, State: "open"}
	readIssueRequest(r).applyTo(&issue)
	f.issues = append(f.issues, issue)
	writeJSON(w, http.StatusCreated, issue)
}

func (f *fakeGitHub) getIssue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if issue := f.requestedIssue(w, r); issue != nil {
		writeJSON(w, http.StatusOK, issue)
	}
}

// updateIssue answers an update, which also closes issues.
func (f *fakeGitHub) updateIssue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.requestedIssue(w, r)
	if issue == nil {
		return
	}
	if f.failUpdate {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
		return
	}
	readIssueRequest(r).applyTo(issue)
	writeJSON(w, http.StatusOK, issue)
}

// timeline answers an issue timeline with no events.
func (f *fakeGitHub) timeline(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.requestedIssue(w, r) != nil {
		writeJSON(w, http.StatusOK, []any{})
	}
}

// requestedIssue returns the issue the request path names. When GitHub wouldn't return it, it writes the
// answer GitHub gives instead (the unavailable status, or 404) and returns nil. f.mu must be held.
func (f *fakeGitHub) requestedIssue(w http.ResponseWriter, r *http.Request) *github.IssueResponse {
	number, _ := strconv.Atoi(r.PathValue("number"))
	if status, ok := f.unavailable[number]; ok {
		writeJSON(w, status, map[string]string{"message": http.StatusText(status)})
		return nil
	}
	if number < 1 || number > len(f.issues) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
		return nil
	}
	return &f.issues[number-1]
}

// issueRequest is the body the client sends to create, update or close an issue. Nil means left out.
type issueRequest struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
	State *string `json:"state"`
}

func readIssueRequest(r *http.Request) issueRequest {
	var req issueRequest
	_ = json.NewDecoder(r.Body).Decode(&req)
	return req
}

// applyTo copies the fields the client sent to issue.
func (req issueRequest) applyTo(issue *github.IssueResponse) {
	if req.Title != nil {
		issue.Title = *req.Title
	}
	if req.Body != nil {
		issue.Body = *req.Body
	}
	if req.State != nil {
		issue.State = *req.State
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
