package github

import (
	"context"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// ctx is the context of the client's requests in these tests.
var ctx = context.Background()

func TestGitHub(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "GitHub Client Suite")
}
