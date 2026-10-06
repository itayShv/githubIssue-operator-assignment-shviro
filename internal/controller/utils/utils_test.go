package utils

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
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

var _ = Describe("ParseRepoURL", func() {
	DescribeTable("should split a repository URL into owner and repo",
		func(url, owner, repo string) {
			gotOwner, gotRepo, err := ParseRepoURL(url)
			Expect(err).NotTo(HaveOccurred())
			Expect(gotOwner).To(Equal(owner))
			Expect(gotRepo).To(Equal(repo))
		},
		Entry("plain", "https://github.com/itay/foo", "itay", "foo"),
		Entry("with a trailing slash", "https://github.com/dana-team/bar/", "dana-team", "bar"),
	)

	DescribeTable("should reject a URL that isn't a repository",
		func(url string) {
			_, _, err := ParseRepoURL(url)
			Expect(err).To(HaveOccurred())
		},
		Entry("owner only", "https://github.com/itay"),
		Entry("a page inside the repository", "https://github.com/itay/foo/issues"),
	)
})

var _ = Describe("SameRepo", func() {
	DescribeTable("should compare owner and repo, ignoring letter case and a trailing slash",
		func(a, b string, same bool) {
			Expect(SameRepo(a, b)).To(Equal(same))
		},
		Entry("identical", otherRepoURL, otherRepoURL, true),
		Entry("different letter case and a trailing slash", repoURL, "https://github.com/Test-Owner/Test-Repo/", true),
		Entry("another repository", repoURL, otherRepoURL, false),
		Entry("an invalid URL", repoURL, "https://github.com/test-owner", false),
	)
})

var _ = Describe("IsOlder", func() {
	It("should compare creation times", func() {
		a, b := newCR("a", repoURL, testTitle, 0, ""), newCR("b", repoURL, testTitle, 1, "")
		Expect(IsOlder(a, b)).To(BeTrue())
		Expect(IsOlder(b, a)).To(BeFalse())
	})

	It("should compare namespace/name when the creation times are equal", func() {
		a, b := newCR("b", repoURL, testTitle, 0, ""), newCR("c", repoURL, testTitle, 0, "")
		Expect(IsOlder(a, b)).To(BeTrue())
		Expect(IsOlder(b, a)).To(BeFalse())
	})
})

var _ = Describe("ManagedIssueNumber", func() {
	DescribeTable("should not count a missing or invalid annotation",
		func(annotation string) {
			cr := newCR("cr", repoURL, testTitle, 0, annotation)
			_, ok, err := ManagedIssueNumber(ctx, readerWith(cr), cr)
			Expect(err).NotTo(HaveOccurred())
			Expect(ok).To(BeFalse())
		},
		Entry("missing", ""),
		Entry("not a number", "abc"),
		Entry("zero", "0"),
		Entry("negative", "-3"),
	)

	It("should return the issue the CR manages", func() {
		cr := newCR("cr", repoURL, testTitle, 1, "42")
		newerCopy := newCR("newer-copy", repoURL, testTitle, 2, "42")
		olderOtherRepo := newCR("older-other-repo", otherRepoURL, testTitle, 0, "42")

		number, ok, err := ManagedIssueNumber(ctx, readerWith(cr, newerCopy, olderOtherRepo), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeTrue())
		Expect(number).To(Equal(42))
	})

	It("should treat the annotation as a copy when an older CR has the same repo and number", func() {
		original := newCR("original", repoURL, testTitle, 0, "42")
		cr := newCR("cr", repoURL, testTitle, 1, "42")

		_, ok, err := ManagedIssueNumber(ctx, readerWith(original, cr), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("CheckTitleClaim", func() {
	It("should let a CR link when no CR comes first, and return the issues other CRs manage", func() {
		cr := newCR("cr", repoURL, testTitle, 1, "")
		newerSameTitle := newCR("newer-same-title", repoURL, testTitle, 2, "")
		otherTitle := newCR("other-title", repoURL, "Other", 0, "7")
		otherRepo := newCR("other-repo", otherRepoURL, testTitle, 0, "8")

		blocker, claimed, err := CheckTitleClaim(ctx, readerWith(cr, newerSameTitle, otherTitle, otherRepo), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocker).To(BeNil())
		Expect(claimed).To(Equal(map[int]bool{7: true}))
	})

	It("should block a CR when a CR with the same title is linked, even a newer one", func() {
		cr := newCR("cr", repoURL, testTitle, 0, "")
		linked := newCR("linked", repoURL, testTitle, 1, "42")

		blocker, claimed, err := CheckTitleClaim(ctx, readerWith(cr, linked), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocker).NotTo(BeNil())
		Expect(blocker.Name).To(Equal("linked"))
		Expect(claimed).To(Equal(map[int]bool{42: true}))
	})

	It("should block a CR when an older CR with the same title isn't linked yet", func() {
		older := newCR("older", repoURL, testTitle, 0, "")
		cr := newCR("cr", repoURL, testTitle, 1, "")

		blocker, claimed, err := CheckTitleClaim(ctx, readerWith(older, cr), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocker).NotTo(BeNil())
		Expect(blocker.Name).To(Equal("older"))
		Expect(claimed).To(BeEmpty())
	})

	It("should look in every namespace and return the oldest blocker", func() {
		cr := newCR("cr", repoURL, testTitle, 2, "")
		linked := newCR("linked", repoURL, testTitle, 1, "42")
		oldest := newCR("oldest", repoURL, testTitle, 0, "")
		oldest.Namespace = "team2"

		blocker, _, err := CheckTitleClaim(ctx, readerWith(cr, linked, oldest), cr)
		Expect(err).NotTo(HaveOccurred())
		Expect(blocker).NotTo(BeNil())
		Expect(blocker.Namespace + "/" + blocker.Name).To(Equal("team2/oldest"))
	})
})

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

// readerWith returns a reader that holds the given CRs, like the controller's cache or API reader.
func readerWith(crs ...*githubv1alpha1.GithubIssue) client.Reader {
	objs := make([]client.Object, 0, len(crs))
	for _, cr := range crs {
		objs = append(objs, cr)
	}
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objs...).Build()
}
