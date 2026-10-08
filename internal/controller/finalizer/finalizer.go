package finalizer

import (
	"context"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// Remove removes a finalizer from the github issue object.
func Remove(ctx context.Context, gitHubIssueCR *githubv1alpha1.GithubIssue, k8sClient client.Client) error {
	controllerutil.RemoveFinalizer(gitHubIssueCR, utils.GitHubIssueDeletionFinalizer)
	if err := k8sClient.Update(ctx, gitHubIssueCR); err != nil {
		return err
	}
	return nil
}

// Ensure adds a finalizer to the github issue object if one does not exist.
func Ensure(ctx context.Context, gitHubIssueCR *githubv1alpha1.GithubIssue, k8sClient client.Client) error {
	if !controllerutil.ContainsFinalizer(gitHubIssueCR, utils.GitHubIssueDeletionFinalizer) {
		controllerutil.AddFinalizer(gitHubIssueCR, utils.GitHubIssueDeletionFinalizer)
		if err := k8sClient.Update(ctx, gitHubIssueCR); err != nil {
			return err
		}
	}
	return nil
}
