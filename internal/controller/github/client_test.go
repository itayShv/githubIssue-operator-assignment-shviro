package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

var _ = Describe("Client", func() {
	const owner, repo, testTitle = "test-owner", "test-repo", "Login bug"
	const connected, disconnected = utils.GitHubEventConnected, utils.GitHubEventDisconnected

	var (
		server *testServer
		client *Client
	)

	BeforeEach(func() {
		server = newTestServer()
		client = NewClient(server.URL, "test-token")
	})

	AfterEach(func() {
		server.Close()
	})

	It("should send the token, the media type and the pinned API version", func() {
		server.answer(jsonResponse(http.StatusOK, openIssue(7, testTitle)))
		_, err := client.GetIssue(ctx, owner, repo, 7)
		Expect(err).NotTo(HaveOccurred())

		Expect(server.requests()).To(HaveLen(1))
		req := server.requests()[0]
		Expect(req.Method).To(Equal(http.MethodGet))
		Expect(req.Path).To(Equal("/repos/test-owner/test-repo/issues/7"))
		Expect(req.Header.Get("Authorization")).To(Equal("Bearer test-token"))
		Expect(req.Header.Get("Accept")).To(Equal(utils.GitHubMediaType))
		Expect(req.Header.Get(utils.GitHubAPIVersionHeader)).To(Equal(utils.GitHubAPIVersion))
	})

	Describe("FindIssueByTitle", func() {
		It("should return the open issue with the exact title, skipping pull requests and claimed issues", func() {
			server.answer(pages([]IssueResponse{
				{Number: 1, Title: testTitle, State: "open", PullRequest: &struct{}{}}, // a pull request
				openIssue(2, testTitle),   // managed by another CR
				openIssue(3, "login bug"), // the title differs in letter case
				openIssue(4, testTitle),
			}))
			issue, err := client.FindIssueByTitle(ctx, owner, repo, testTitle, map[int]bool{2: true})
			Expect(err).NotTo(HaveOccurred())
			Expect(issue).NotTo(BeNil())
			Expect(issue.Number).To(Equal(4))

			req := server.requests()[0]
			Expect(req.Query.Get("state")).To(Equal("open"))
			Expect(req.Query.Get("per_page")).To(Equal(strconv.Itoa(utils.GitHubPageSize)))
		})

		It("should look through the next pages", func() {
			server.answer(pages([]IssueResponse{openIssue(1, "Other")}, []IssueResponse{openIssue(2, testTitle)}))
			issue, err := client.FindIssueByTitle(ctx, owner, repo, testTitle, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(issue).NotTo(BeNil())
			Expect(issue.Number).To(Equal(2))
			Expect(server.requests()).To(HaveLen(2))
		})

		It("should return nil when no open issue has the title", func() {
			server.answer(pages([]IssueResponse{openIssue(1, "Other")}))
			issue, err := client.FindIssueByTitle(ctx, owner, repo, testTitle, nil)
			Expect(err).NotTo(HaveOccurred())
			Expect(issue).To(BeNil())
			Expect(server.requests()).To(HaveLen(2), "page 1, then an empty page 2")
		})
	})

	Describe("writing issues", func() {
		It("should create an issue with its title and body, sending even an empty body", func() {
			server.answer(jsonResponse(http.StatusCreated, openIssue(5, "New")))
			issue, err := client.CreateIssue(ctx, owner, repo, "New", "")
			Expect(err).NotTo(HaveOccurred())
			Expect(issue.Number).To(Equal(5))

			req := server.requests()[0]
			Expect(req.Method).To(Equal(http.MethodPost))
			Expect(req.Path).To(Equal("/repos/test-owner/test-repo/issues"))
			Expect(req.Body).To(Equal(map[string]any{"title": "New", "body": ""}))
		})

		It("should update only the title and body, never the state", func() {
			server.answer(jsonResponse(http.StatusOK, openIssue(5, "Title")))
			_, err := client.UpdateIssue(ctx, owner, repo, 5, "Title", "Body")
			Expect(err).NotTo(HaveOccurred())

			req := server.requests()[0]
			Expect(req.Method).To(Equal(http.MethodPatch))
			Expect(req.Path).To(Equal("/repos/test-owner/test-repo/issues/5"))
			Expect(req.Body).To(Equal(map[string]any{"title": "Title", "body": "Body"}))
		})

		It("should close an issue by sending only its state", func() {
			server.answer(jsonResponse(http.StatusOK, IssueResponse{Number: 5, State: utils.GitHubIssueStateClosed}))
			Expect(client.CloseIssue(ctx, owner, repo, 5)).To(Succeed())

			req := server.requests()[0]
			Expect(req.Method).To(Equal(http.MethodPatch))
			Expect(req.Path).To(Equal("/repos/test-owner/test-repo/issues/5"))
			Expect(req.Body).To(Equal(map[string]any{"state": utils.GitHubIssueStateClosed}))
		})
	})

	Describe("errors", func() {
		DescribeTable("should return the matching error, naming the issue",
			func(status int, sentinel error, message string) {
				server.answer(func(w http.ResponseWriter, r *http.Request) {
					if status == http.StatusMovedPermanently {
						w.Header().Set("Location", "/elsewhere")
					}
					jsonResponse(status, map[string]string{"message": http.StatusText(status)})(w, r)
				})
				_, err := client.GetIssue(ctx, owner, repo, 7)
				Expect(err).To(MatchError(sentinel))
				Expect(err).To(MatchError(ContainSubstring(message)))
				Expect(server.requests()).To(HaveLen(1), "redirects must not be followed")
			},
			Entry("404", http.StatusNotFound, ErrNotFound, "github issue test-owner/test-repo#7: not found"),
			Entry("410", http.StatusGone, ErrGone, "github issue test-owner/test-repo#7: gone"),
			Entry("301, without following the redirect", http.StatusMovedPermanently, ErrMoved,
				"github issue test-owner/test-repo#7: moved"),
		)

		It("should name the repository when a repository request fails", func() {
			server.answer(jsonResponse(http.StatusGone, map[string]string{"message": "Issues are disabled for this repo"}))
			_, err := client.FindIssueByTitle(ctx, owner, repo, testTitle, nil)
			Expect(err).To(MatchError(ErrGone))
			Expect(err).To(MatchError(ContainSubstring("github repository test-owner/test-repo: gone")))
		})

		It("should keep GitHub's message for any other error", func() {
			server.answer(jsonResponse(http.StatusUnauthorized, map[string]string{"message": "Bad credentials"}))
			_, err := client.GetIssue(ctx, owner, repo, 7)
			Expect(err).To(MatchError(ContainSubstring(`returned 401: {"message":"Bad credentials"}`)))
			Expect(err).NotTo(MatchError(ErrNotFound))
			Expect(err).NotTo(MatchError(ErrGone))
			Expect(err).NotTo(MatchError(ErrMoved))
		})
	})

	Describe("HasLinkedPullRequest", func() {
		DescribeTable("should count pull requests linked and unlinked in the Development sidebar",
			func(events []string, linked bool) {
				server.answer(pages(timeline(events...)))
				got, err := client.HasLinkedPullRequest(ctx, owner, repo, 7)
				Expect(err).NotTo(HaveOccurred())
				Expect(got).To(Equal(linked))
				Expect(server.requests()).To(HaveLen(1), "a short page is the last one")
			},
			Entry("no events", []string{}, false),
			Entry("linked", []string{connected}, true),
			Entry("linked, then unlinked", []string{connected, disconnected}, false),
			Entry("two linked, one unlinked", []string{connected, connected, disconnected}, true),
			Entry("only other events, such as a pull request mentioning the issue", []string{"cross-referenced", "labeled"}, false),
		)

		It("should read the next page after a full one", func() {
			full := timeline(slices.Repeat([]string{"labeled"}, utils.GitHubPageSize-1)...)
			full = append(full, timelineEvent{Event: connected})
			server.answer(pages(full, timeline(disconnected)))

			linked, err := client.HasLinkedPullRequest(ctx, owner, repo, 7)
			Expect(err).NotTo(HaveOccurred())
			Expect(linked).To(BeFalse(), "the link on page 1 is removed on page 2")
			Expect(server.requests()).To(HaveLen(2))
		})
	})
})

// receivedRequest is what the test server saw of one request.
type receivedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

// testServer stands in for GitHub. Each test sets its answer with answer, and it records every request.
type testServer struct {
	*httptest.Server

	mu       sync.Mutex
	respond  http.HandlerFunc
	received []receivedRequest
}

func newTestServer() *testServer {
	s := &testServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := receivedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		s.mu.Lock()
		s.received = append(s.received, req)
		respond := s.respond
		s.mu.Unlock()
		respond(w, r)
	}))
	return s
}

// answer sets how the server answers every request.
func (s *testServer) answer(respond http.HandlerFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.respond = respond
}

// requests returns the requests received so far.
func (s *testServer) requests() []receivedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.received)
}

// jsonResponse answers with status and v as JSON.
func jsonResponse(status int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
}

// pages answers a paged list request with list[page-1], and with an empty list after the last page.
func pages(list ...any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var v any = []any{}
		if page, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && page >= 1 && page <= len(list) {
			v = list[page-1]
		}
		jsonResponse(http.StatusOK, v)(w, r)
	}
}

// openIssue returns an open issue as GitHub returns it.
func openIssue(number int, title string) IssueResponse {
	return IssueResponse{Number: number, Title: title, State: "open"}
}

// timeline returns issue timeline events with the given names.
func timeline(names ...string) []timelineEvent {
	events := make([]timelineEvent, 0, len(names))
	for _, name := range names {
		events = append(events, timelineEvent{Event: name})
	}
	return events
}
