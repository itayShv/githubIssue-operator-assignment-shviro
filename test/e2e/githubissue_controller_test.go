//go:build e2e
// +build e2e

/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
	"github.com/itayshviro/githubissue-operator/internal/controller"
	"github.com/itayshviro/githubissue-operator/internal/controller/github"
	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

const (
	testNamespace  = "default"
	testRepoURL    = "https://github.com/test-owner/test-repo"
	issueStateOpen = "open"
)

var _ = Describe("GithubIssue Controller", func() {
	var (
		fakeGH     *fakeGitHub
		reconciler *controller.GithubIssueReconciler
	)

	reconcileCR := func(name string) (reconcile.Result, error) {
		return reconciler.Reconcile(ctx, reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: testNamespace, Name: name},
		})
	}

	BeforeEach(func() {
		fakeGH = newFakeGitHub()
		reconciler = &controller.GithubIssueReconciler{
			Client:       k8sClient,
			Scheme:       k8sClient.Scheme(),
			GitHubToken:  "test-token",
			GitHubAPIURL: fakeGH.server.URL,
		}
	})

	AfterEach(func() {
		fakeGH.server.Close()

		By("removing every GithubIssue, since duplicate detection looks at all of them")
		var list githubv1alpha1.GithubIssueList
		Expect(k8sClient.List(ctx, &list)).To(Succeed())
		for i := range list.Items {
			cr := &list.Items[i]
			if controllerutil.RemoveFinalizer(cr, utils.GitHubIssueDeletionFinalizer) {
				Expect(k8sClient.Update(ctx, cr)).To(Succeed())
			}
			Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, cr))).To(Succeed())
		}
	})

	It("should create a GitHub issue when no open issue has the title", func() {
		const name = "create-issue"
		Expect(k8sClient.Create(ctx, newGithubIssue(name, "Login fails on Safari", "Steps to reproduce"))).To(Succeed())

		result, err := reconcileCR(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(utils.ResyncPeriod))

		By("checking the issue was created on GitHub")
		Expect(fakeGH.issueCount()).To(Equal(1))
		issue := fakeGH.issue(1)
		Expect(issue.Title).To(Equal("Login fails on Safari"))
		Expect(issue.Body).To(Equal("Steps to reproduce"))
		Expect(issue.State).To(Equal(issueStateOpen))

		By("checking the CR is linked to the issue and reports it")
		cr := getGithubIssue(name)
		Expect(cr.Annotations).To(HaveKeyWithValue(utils.IssueNumberAnnotation, "1"))
		Expect(cr.Finalizers).To(ContainElement(utils.GitHubIssueDeletionFinalizer))
		expectCondition(cr, utils.ConditionReady, metav1.ConditionTrue, utils.ReasonSynced)
		expectCondition(cr, utils.ConditionIssueOpen, metav1.ConditionTrue, utils.ReasonOpen)
		expectCondition(cr, utils.ConditionIssueHasPR, metav1.ConditionFalse, utils.ReasonNoPullRequest)
	})

	It("should report the error when GitHub fails to create the issue", func() {
		const name = "create-fails"
		fakeGH.failCreate.Store(true)
		Expect(k8sClient.Create(ctx, newGithubIssue(name, "Create fails", "Not created"))).To(Succeed())

		_, err := reconcileCR(name)
		Expect(err).To(MatchError(ContainSubstring("403")))
		Expect(fakeGH.issueCount()).To(BeZero())

		By("checking the CR is not linked and reports the failure")
		cr := getGithubIssue(name)
		Expect(cr.Annotations).NotTo(HaveKey(utils.IssueNumberAnnotation))
		expectCondition(cr, utils.ConditionReady, metav1.ConditionFalse, utils.ReasonReconcileFailed)
		Expect(meta.FindStatusCondition(cr.Status.Conditions, utils.ConditionIssueOpen)).To(BeNil())
	})

	It("should report the error when GitHub fails to update the issue", func() {
		const name = "update-fails"
		number := fakeGH.addIssue("Update fails", "Old description")
		fakeGH.failUpdate.Store(true)
		Expect(k8sClient.Create(ctx, newGithubIssue(name, "Update fails", "New description"))).To(Succeed())

		_, err := reconcileCR(name)
		Expect(err).To(MatchError(ContainSubstring("403")))

		By("checking the existing issue was adopted but not changed")
		Expect(fakeGH.issueCount()).To(Equal(1))
		Expect(fakeGH.issue(number).Body).To(Equal("Old description"))
		cr := getGithubIssue(name)
		Expect(cr.Annotations).To(HaveKeyWithValue(utils.IssueNumberAnnotation, strconv.Itoa(number)))
		expectCondition(cr, utils.ConditionReady, metav1.ConditionFalse, utils.ReasonReconcileFailed)
	})

	It("should close the GitHub issue when the CR is deleted", func() {
		const name = "close-on-delete"
		Expect(k8sClient.Create(ctx, newGithubIssue(name, "Close on delete", "Closed with the CR"))).To(Succeed())
		_, err := reconcileCR(name)
		Expect(err).NotTo(HaveOccurred())
		Expect(fakeGH.issue(1).State).To(Equal(issueStateOpen))

		By("deleting the CR and reconciling the deletion")
		Expect(k8sClient.Delete(ctx, getGithubIssue(name))).To(Succeed())
		_, err = reconcileCR(name)
		Expect(err).NotTo(HaveOccurred())

		By("checking the issue was closed and the CR is gone")
		Expect(fakeGH.issue(1).State).To(Equal(utils.GitHubIssueStateClosed))
		err = k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, &githubv1alpha1.GithubIssue{})
		Expect(apierrors.IsNotFound(err)).To(BeTrue())
	})

	DescribeTable("should keep the link and report why when GitHub no longer returns the issue",
		func(name string, status int, readyReason, reason string, open metav1.ConditionStatus, message string) {
			Expect(k8sClient.Create(ctx, newGithubIssue(name, "Missing on GitHub", "Was here"))).To(Succeed())
			_, err := reconcileCR(name)
			Expect(err).NotTo(HaveOccurred())
			linked := getGithubIssue(name).Annotations[utils.IssueNumberAnnotation]
			number, err := strconv.Atoi(linked)
			Expect(err).NotTo(HaveOccurred())

			By("making GitHub answer " + http.StatusText(status) + " for the linked issue")
			fakeGH.makeUnavailable(number, status)
			result, err := reconcileCR(name)
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(utils.ResyncPeriod))

			By("checking the link is kept, no issue is created, and the status says why")
			cr := getGithubIssue(name)
			Expect(cr.Annotations).To(HaveKeyWithValue(utils.IssueNumberAnnotation, linked))
			Expect(fakeGH.issueCount()).To(Equal(1))
			expectCondition(cr, utils.ConditionReady, metav1.ConditionFalse, readyReason)
			expectCondition(cr, utils.ConditionIssueOpen, open, reason)
			expectCondition(cr, utils.ConditionIssueHasPR, metav1.ConditionUnknown, reason)
			Expect(meta.FindStatusCondition(cr.Status.Conditions, utils.ConditionReady).Message).To(ContainSubstring(message))
		},
		Entry("deleted (410)", "deleted-issue", http.StatusGone,
			utils.ReasonIssueDeleted, utils.ReasonDeleted, metav1.ConditionFalse, "was deleted"),
		Entry("moved (301), without following the redirect", "moved-issue", http.StatusMovedPermanently,
			utils.ReasonIssueDeleted, utils.ReasonDeleted, metav1.ConditionFalse, "was moved"),
		Entry("not found (404)", "not-found-issue", http.StatusNotFound,
			utils.ReasonIssueNotFound, utils.ReasonNotFound, metav1.ConditionUnknown, "not found"),
	)

	It("should report a duplicate CR and create no second issue", func() {
		const first, second = "dup-first", "dup-second"
		Expect(k8sClient.Create(ctx, newGithubIssue(first, "Same title", "First"))).To(Succeed())
		_, err := reconcileCR(first)
		Expect(err).NotTo(HaveOccurred())

		By("creating a second CR with the same repo and title")
		Expect(k8sClient.Create(ctx, newGithubIssue(second, "Same title", "Second"))).To(Succeed())
		result, err := reconcileCR(second)
		Expect(err).NotTo(HaveOccurred())
		Expect(result.RequeueAfter).To(Equal(utils.ResyncPeriod))

		By("checking only the first CR has an issue and the second one waits")
		Expect(fakeGH.issueCount()).To(Equal(1))
		cr := getGithubIssue(second)
		Expect(cr.Annotations).NotTo(HaveKey(utils.IssueNumberAnnotation))
		expectCondition(cr, utils.ConditionReady, metav1.ConditionFalse, utils.ReasonDuplicateIssue)
		Expect(meta.FindStatusCondition(cr.Status.Conditions, utils.ConditionReady).Message).
			To(Equal("GithubIssue default/dup-first comes first for this repo and title"))
		Expect(meta.FindStatusCondition(cr.Status.Conditions, utils.ConditionIssueOpen)).To(BeNil())
		Expect(meta.FindStatusCondition(cr.Status.Conditions, utils.ConditionIssueHasPR)).To(BeNil())
	})
})

// newGithubIssue returns a GithubIssue CR for the test repository.
func newGithubIssue(name, title, description string) *githubv1alpha1.GithubIssue {
	return &githubv1alpha1.GithubIssue{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: testNamespace},
		Spec:       githubv1alpha1.GithubIssueSpec{Repo: testRepoURL, Title: title, Description: description},
	}
}

// getGithubIssue reads the GithubIssue CR from the API server.
func getGithubIssue(name string) *githubv1alpha1.GithubIssue {
	GinkgoHelper()
	cr := &githubv1alpha1.GithubIssue{}
	Expect(k8sClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: name}, cr)).To(Succeed())
	return cr
}

// expectCondition checks that the CR has the condition with this status and reason, and a lastTransitionTime.
func expectCondition(cr *githubv1alpha1.GithubIssue, condType string, status metav1.ConditionStatus, reason string) {
	GinkgoHelper()
	c := meta.FindStatusCondition(cr.Status.Conditions, condType)
	Expect(c).NotTo(BeNil(), "condition %s is missing", condType)
	Expect(c.Status).To(Equal(status))
	Expect(c.Reason).To(Equal(reason))
	Expect(c.LastTransitionTime.IsZero()).To(BeFalse())
}

// fakeGitHub is an in-memory stand-in for the GitHub issues REST API, so the tests never call GitHub.
// It holds the issues of one repository; the owner and repo in request paths are ignored.
type fakeGitHub struct {
	server *httptest.Server
	// failCreate and failUpdate make creating or updating (which includes closing) an issue answer 403,
	// as GitHub does for a token without write access.
	failCreate atomic.Bool
	failUpdate atomic.Bool

	mu sync.Mutex
	// issues[i] is issue number i+1
	issues []*github.IssueResponse
	// unavailable maps an issue number to the status GitHub answers for it instead of the issue:
	// 410 after a delete, 301 after a transfer, 404 when the token can't see it.
	unavailable map[int]int
}

// issueRequestBody is the body the operator sends to create or update an issue. Nil means the field was left out.
type issueRequestBody struct {
	Title *string `json:"title"`
	Body  *string `json:"body"`
	State *string `json:"state"`
}

func newFakeGitHub() *fakeGitHub {
	f := &fakeGitHub{unavailable: map[int]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues", f.listIssues)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", f.createIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}", f.getIssue)
	mux.HandleFunc("PATCH /repos/{owner}/{repo}/issues/{number}", f.updateIssue)
	mux.HandleFunc("GET /repos/{owner}/{repo}/issues/{number}/timeline", f.timeline)
	f.server = httptest.NewServer(mux)
	return f
}

// addIssue adds an open issue, as if someone had created it on GitHub, and returns its number.
func (f *fakeGitHub) addIssue(title, body string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.add(title, body).Number
}

// makeUnavailable makes GitHub answer status for the issue instead of returning it: 410 as after a delete,
// 301 as after a transfer to another repository, or 404 as when the token can't see it. The issue is also
// left out of listings.
func (f *fakeGitHub) makeUnavailable(number, status int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.unavailable[number] = status
}

// issue returns a copy of the issue with the given number, or nil if there is none.
func (f *fakeGitHub) issue(number int) *github.IssueResponse {
	f.mu.Lock()
	defer f.mu.Unlock()
	issue := f.lookup(number)
	if issue == nil {
		return nil
	}
	issueCopy := *issue
	return &issueCopy
}

func (f *fakeGitHub) issueCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.issues)
}

// add appends an open issue. f.mu must be held.
func (f *fakeGitHub) add(title, body string) *github.IssueResponse {
	issue := &github.IssueResponse{Number: len(f.issues) + 1, Title: title, Body: body, State: issueStateOpen}
	f.issues = append(f.issues, issue)
	return issue
}

// lookup returns the issue with the given number, or nil if there is none. f.mu must be held.
func (f *fakeGitHub) lookup(number int) *github.IssueResponse {
	if number < 1 || number > len(f.issues) {
		return nil
	}
	return f.issues[number-1]
}

// requestedIssue returns the issue named by the request's {number}, or nil if there is none. f.mu must be held.
func (f *fakeGitHub) requestedIssue(r *http.Request) *github.IssueResponse {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		return nil
	}
	return f.lookup(number)
}

// writeUnavailable answers for an issue made unavailable with makeUnavailable, and reports whether it did.
// f.mu must be held.
func (f *fakeGitHub) writeUnavailable(w http.ResponseWriter, r *http.Request) bool {
	number, err := strconv.Atoi(r.PathValue("number"))
	if err != nil {
		return false
	}
	status, ok := f.unavailable[number]
	if !ok {
		return false
	}
	if status == http.StatusMovedPermanently {
		// Where the issue moved to. The operator must not follow it: this fake has no such route and would
		// answer 404, so a test expecting "moved" would fail.
		w.Header().Set("Location", "/repositories/1/issues/1")
	}
	writeJSON(w, status, map[string]string{"message": http.StatusText(status)})
	return true
}

// listIssues answers GET /repos/{owner}/{repo}/issues?state=open. All issues fit on page 1.
func (f *fakeGitHub) listIssues(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	open := make([]github.IssueResponse, 0, len(f.issues))
	if r.URL.Query().Get("page") == "1" {
		for _, issue := range f.issues {
			if issue.State == issueStateOpen && f.unavailable[issue.Number] == 0 {
				open = append(open, *issue)
			}
		}
	}
	writeJSON(w, http.StatusOK, open)
}

// createIssue answers POST /repos/{owner}/{repo}/issues.
func (f *fakeGitHub) createIssue(w http.ResponseWriter, r *http.Request) {
	if f.failCreate.Load() {
		writeForbidden(w)
		return
	}
	var req issueRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Title == nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	body := ""
	if req.Body != nil {
		body = *req.Body
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	writeJSON(w, http.StatusCreated, f.add(*req.Title, body))
}

// getIssue answers GET /repos/{owner}/{repo}/issues/{number}.
func (f *fakeGitHub) getIssue(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeUnavailable(w, r) {
		return
	}
	issue := f.requestedIssue(r)
	if issue == nil {
		writeNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

// updateIssue answers PATCH /repos/{owner}/{repo}/issues/{number}, which also closes issues.
func (f *fakeGitHub) updateIssue(w http.ResponseWriter, r *http.Request) {
	if f.failUpdate.Load() {
		writeForbidden(w)
		return
	}
	var req issueRequestBody
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"message": "Invalid request"})
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeUnavailable(w, r) {
		return
	}
	issue := f.requestedIssue(r)
	if issue == nil {
		writeNotFound(w)
		return
	}
	if req.Title != nil {
		issue.Title = *req.Title
	}
	if req.Body != nil {
		issue.Body = *req.Body
	}
	if req.State != nil {
		issue.State = *req.State
	}
	writeJSON(w, http.StatusOK, issue)
}

// timeline answers GET /repos/{owner}/{repo}/issues/{number}/timeline with no events.
func (f *fakeGitHub) timeline(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.writeUnavailable(w, r) {
		return
	}
	if f.requestedIssue(r) == nil {
		writeNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, []any{})
}

func writeForbidden(w http.ResponseWriter) {
	writeJSON(w, http.StatusForbidden, map[string]string{"message": "Resource not accessible by personal access token"})
}

func writeNotFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"message": "Not Found"})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
