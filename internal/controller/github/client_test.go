package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

const (
	testOwner = "test-owner"
	testRepo  = "test-repo"
	testTitle = "Login bug"
	testToken = "test-token"
)

func TestIssueRequests(t *testing.T) {
	// The server answers every request with this issue.
	response := openIssue(5, testTitle)

	tests := []struct {
		name           string
		call           func(ctx context.Context, c *Client) (*IssueResponse, error)
		responseStatus int
		expectedMethod string
		expectedPath   string
		expectedBody   map[string]any
		expectedIssue  *IssueResponse
	}{
		{
			name: "GetIssue",
			call: func(ctx context.Context, c *Client) (*IssueResponse, error) {
				return c.GetIssue(ctx, testOwner, testRepo, 5)
			},
			responseStatus: http.StatusOK,
			expectedMethod: http.MethodGet,
			expectedPath:   "/repos/test-owner/test-repo/issues/5",
			expectedBody:   nil,
			expectedIssue:  &response,
		},
		{
			name: "CreateIssue sends the title and body, even an empty body",
			call: func(ctx context.Context, c *Client) (*IssueResponse, error) {
				return c.CreateIssue(ctx, testOwner, testRepo, "New", "")
			},
			responseStatus: http.StatusCreated,
			expectedMethod: http.MethodPost,
			expectedPath:   "/repos/test-owner/test-repo/issues",
			expectedBody:   map[string]any{"title": "New", "body": ""},
			expectedIssue:  &response,
		},
		{
			name: "UpdateIssue sends only the title and body, never the state",
			call: func(ctx context.Context, c *Client) (*IssueResponse, error) {
				return c.UpdateIssue(ctx, testOwner, testRepo, 5, "Title", "Body")
			},
			responseStatus: http.StatusOK,
			expectedMethod: http.MethodPatch,
			expectedPath:   "/repos/test-owner/test-repo/issues/5",
			expectedBody:   map[string]any{"title": "Title", "body": "Body"},
			expectedIssue:  &response,
		},
		{
			name: "CloseIssue sends only the state",
			call: func(ctx context.Context, c *Client) (*IssueResponse, error) {
				return nil, c.CloseIssue(ctx, testOwner, testRepo, 5)
			},
			responseStatus: http.StatusOK,
			expectedMethod: http.MethodPatch,
			expectedPath:   "/repos/test-owner/test-repo/issues/5",
			expectedBody:   map[string]any{"state": utils.GitHubIssueStateClosed},
			expectedIssue:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, jsonResponse(tt.responseStatus, response))
			issue, err := tt.call(t.Context(), server.client())
			require.NoError(t, err)
			assert.Equal(t, tt.expectedIssue, issue)

			requests := server.requests()
			require.Len(t, requests, 1)
			assert.Equal(t, tt.expectedMethod, requests[0].Method)
			assert.Equal(t, tt.expectedPath, requests[0].Path)
			assert.Equal(t, tt.expectedBody, requests[0].Body)
			assert.Equal(t, "Bearer "+testToken, requests[0].Header.Get("Authorization"))
			assert.Equal(t, utils.GitHubMediaType, requests[0].Header.Get("Accept"))
			assert.Equal(t, utils.GitHubAPIVersion, requests[0].Header.Get(utils.GitHubAPIVersionHeader))
		})
	}
}

func TestFindIssueByTitle(t *testing.T) {
	tests := []struct {
		name             string
		pages            [][]IssueResponse
		skip             map[int]bool
		expectedIssue    *IssueResponse
		expectedRequests int
	}{
		{
			name: "skips pull requests, issues in skip, and titles that differ in letter case",
			pages: [][]IssueResponse{{
				{Number: 1, Title: testTitle, State: "open", PullRequest: &struct{}{}}, // a pull request
				openIssue(2, testTitle),   // in skip: managed by another CR
				openIssue(3, "login bug"), // the title differs in letter case
				openIssue(4, testTitle),
			}},
			skip:             map[int]bool{2: true},
			expectedIssue:    &IssueResponse{Number: 4, Title: testTitle, State: "open"},
			expectedRequests: 1,
		},
		{
			name:             "looks through the next pages",
			pages:            [][]IssueResponse{{openIssue(1, "Other")}, {openIssue(2, testTitle)}},
			skip:             nil,
			expectedIssue:    &IssueResponse{Number: 2, Title: testTitle, State: "open"},
			expectedRequests: 2,
		},
		{
			name:             "returns nil after an empty page when no open issue has the title",
			pages:            [][]IssueResponse{{openIssue(1, "Other")}},
			skip:             nil,
			expectedIssue:    nil,
			expectedRequests: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, pages(tt.pages...))
			issue, err := server.client().FindIssueByTitle(t.Context(), testOwner, testRepo, testTitle, tt.skip)
			require.NoError(t, err)
			assert.Equal(t, tt.expectedIssue, issue)

			requests := server.requests()
			assert.Len(t, requests, tt.expectedRequests)
			for i, req := range requests {
				assert.Equal(t, "/repos/test-owner/test-repo/issues", req.Path)
				assert.Equal(t, "open", req.Query.Get("state"))
				assert.Equal(t, strconv.Itoa(utils.GitHubPageSize), req.Query.Get("per_page"))
				assert.Equal(t, strconv.Itoa(i+1), req.Query.Get("page"))
			}
		})
	}
}

func TestErrors(t *testing.T) {
	getIssue := func(ctx context.Context, c *Client) error {
		_, err := c.GetIssue(ctx, testOwner, testRepo, 7)
		return err
	}
	findIssue := func(ctx context.Context, c *Client) error {
		_, err := c.FindIssueByTitle(ctx, testOwner, testRepo, testTitle, nil)
		return err
	}

	tests := []struct {
		name            string
		call            func(ctx context.Context, c *Client) error
		status          int
		expectedErr     error
		expectedMessage string
	}{
		{
			name:            "404 for an issue",
			call:            getIssue,
			status:          http.StatusNotFound,
			expectedErr:     utils.ErrNotFound,
			expectedMessage: "github issue test-owner/test-repo#7: not found",
		},
		{
			name:            "410 for an issue",
			call:            getIssue,
			status:          http.StatusGone,
			expectedErr:     utils.ErrGone,
			expectedMessage: "github issue test-owner/test-repo#7: gone",
		},
		{
			name:            "301 for an issue, without following the redirect",
			call:            getIssue,
			status:          http.StatusMovedPermanently,
			expectedErr:     utils.ErrMoved,
			expectedMessage: "github issue test-owner/test-repo#7: moved",
		},
		{
			name:            "410 for a repository",
			call:            findIssue,
			status:          http.StatusGone,
			expectedErr:     utils.ErrGone,
			expectedMessage: "github repository test-owner/test-repo: gone",
		},
		{
			name:            "any other status keeps GitHub's message",
			call:            getIssue,
			status:          http.StatusUnauthorized,
			expectedErr:     nil,
			expectedMessage: `returned 401: {"message":"Unauthorized"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, errorResponse(tt.status))
			err := tt.call(t.Context(), server.client())
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.expectedMessage)
			// err matches the expected sentinel error, and no other one
			for _, sentinel := range []error{utils.ErrNotFound, utils.ErrGone, utils.ErrMoved} {
				assert.Equal(t, sentinel == tt.expectedErr, errors.Is(err, sentinel), "errors.Is(err, %q)", sentinel)
			}
			assert.Len(t, server.requests(), 1, "redirects must not be followed")
		})
	}
}

func TestHasLinkedPullRequest(t *testing.T) {
	const connected, disconnected = utils.GitHubEventConnected, utils.GitHubEventDisconnected
	// fullPage is a full page of events that ends with a link, so the client asks for the next page.
	fullPage := append(slices.Repeat([]string{"labeled"}, utils.GitHubPageSize-1), connected)

	tests := []struct {
		name             string
		pages            [][]string
		expected         bool
		expectedRequests int
	}{
		{name: "no events", pages: [][]string{{}}, expected: false, expectedRequests: 1},
		{name: "linked", pages: [][]string{{connected}}, expected: true, expectedRequests: 1},
		{name: "linked, then unlinked", pages: [][]string{{connected, disconnected}}, expected: false, expectedRequests: 1},
		{name: "two linked, one unlinked", pages: [][]string{{connected, connected, disconnected}}, expected: true, expectedRequests: 1},
		{name: "only other events, such as a pull request mentioning the issue", pages: [][]string{{"cross-referenced", "labeled"}}, expected: false, expectedRequests: 1},
		{name: "linked on a full page 1, unlinked on page 2", pages: [][]string{fullPage, {disconnected}}, expected: false, expectedRequests: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := newTestServer(t, timelinePages(tt.pages...))
			linked, err := server.client().HasLinkedPullRequest(t.Context(), testOwner, testRepo, 7)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, linked)
			assert.Len(t, server.requests(), tt.expectedRequests, "a page shorter than the page size is the last one")
		})
	}
}

// receivedRequest is what the test server saw of one request.
type receivedRequest struct {
	Method string
	Path   string
	Query  url.Values
	Header http.Header
	Body   map[string]any
}

// testServer stands in for GitHub: it answers every request with the same handler and records the requests.
type testServer struct {
	*httptest.Server

	mu       sync.Mutex
	received []receivedRequest
}

// newTestServer starts a testServer that answers with respond. It is closed when the test ends.
func newTestServer(t *testing.T, respond http.HandlerFunc) *testServer {
	t.Helper()
	s := &testServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		req := receivedRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.Query(), Header: r.Header.Clone()}
		_ = json.NewDecoder(r.Body).Decode(&req.Body)
		s.mu.Lock()
		s.received = append(s.received, req)
		s.mu.Unlock()
		respond(w, r)
	}))
	t.Cleanup(s.Close)
	return s
}

// client returns a Client that sends its requests to the server.
func (s *testServer) client() *Client {
	return NewClient(s.URL, testToken)
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

// errorResponse answers with status and an error body like GitHub's. A 301 also gets a Location header,
// which the client must not follow.
func errorResponse(status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if status == http.StatusMovedPermanently {
			w.Header().Set("Location", "/elsewhere")
		}
		jsonResponse(status, map[string]string{"message": http.StatusText(status)})(w, r)
	}
}

// pages answers a paged list request with list[page-1], and with an empty list after the last page.
func pages[T any](list ...[]T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		v := []T{}
		if page, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && page >= 1 && page <= len(list) {
			v = list[page-1]
		}
		jsonResponse(http.StatusOK, v)(w, r)
	}
}

// timelinePages answers timeline requests with one page of events for each list of event names.
func timelinePages(names ...[]string) http.HandlerFunc {
	list := make([][]timelineEvent, 0, len(names))
	for _, page := range names {
		events := make([]timelineEvent, 0, len(page))
		for _, name := range page {
			events = append(events, timelineEvent{Event: name})
		}
		list = append(list, events)
	}
	return pages(list...)
}

// openIssue returns an open issue as GitHub returns it.
func openIssue(number int, title string) IssueResponse {
	return IssueResponse{Number: number, Title: title, State: "open"}
}
