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

package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
	"github.com/itayshviro/githubissue-operator/internal/controller/finalizer"
	"github.com/itayshviro/githubissue-operator/internal/controller/github"
	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

// GithubIssueReconciler reconciles a GithubIssue object
type GithubIssueReconciler struct {
	client.Client
	// APIReader reads straight from the API server, bypassing the cache. It is used when a CR claims
	// an issue, so a claim saved a moment earlier is visible. Nil falls back to Client (e.g. in tests).
	APIReader   client.Reader
	Scheme      *runtime.Scheme
	GitHubToken string
	// GitHubAPIURL overrides the GitHub API base URL (used by tests); empty means api.github.com
	GitHubAPIURL string
}

// +kubebuilder:rbac:groups=github.itayshviro.dev,resources=githubissues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=github.itayshviro.dev,resources=githubissues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=github.itayshviro.dev,resources=githubissues/finalizers,verbs=update

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
// TODO(user): Modify the Reconcile function to compare the state specified by
// the GithubIssue object against the actual cluster state, and then
// perform operations to make the cluster state reflect the state specified by
// the user.
//
// For more details, check Reconcile and its Result here:
// - https://pkg.go.dev/sigs.k8s.io/controller-runtime@v0.23.3/pkg/reconcile
func (r *GithubIssueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	logger.Info("Starting reconcilie")
	gitHubIssueCR := githubv1alpha1.GithubIssue{}
	if err := r.Get(ctx, req.NamespacedName, &gitHubIssueCR); err != nil {
		logger.Info("Couldnt find Github Issue CR:" + req.Name)
		if apierrors.IsNotFound(err) {
			logger.Info("GithubIssue was deleted, nothing to do")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !gitHubIssueCR.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, r.handleDelete(ctx, &gitHubIssueCR)
	}

	if err := finalizer.Ensure(ctx, &gitHubIssueCR, r.Client); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure finalizer in GitHub issue CR: %s", err.Error())
	}

	if err := r.handleUpdate(ctx, &gitHubIssueCR); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: utils.ResyncPeriod}, nil
}

// handleDelete closes the GitHub issue the CR manages, if it manages one, and removes the finalizer so
// Kubernetes can delete the CR.
func (r *GithubIssueReconciler) handleDelete(ctx context.Context, cr *githubv1alpha1.GithubIssue) error {
	if !controllerutil.ContainsFinalizer(cr, utils.GitHubIssueDeletionFinalizer) {
		return nil
	}
	if err := utils.CloseManagedIssue(ctx, r.Client, github.NewClient(r.GitHubAPIURL, r.GitHubToken), cr); err != nil {
		return err
	}

	// Remove the finalizer so Kubernetes can delete the CR
	if err := finalizer.Remove(ctx, cr, r.Client); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Removed finalizer from GithubIssue", "name", cr.Name)
	return nil
}

// handleUpdate makes the GitHub issue match the CR, linking the CR to an issue first if needed, and reports
// the issue in the status.
func (r *GithubIssueReconciler) handleUpdate(ctx context.Context, cr *githubv1alpha1.GithubIssue) error {
	return utils.SyncIssue(ctx, r.Client, r.apiReader(), github.NewClient(r.GitHubAPIURL, r.GitHubToken), cr)
}

func (r *GithubIssueReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

// SetupWithManager sets up the controller with the Manager.
func (r *GithubIssueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&githubv1alpha1.GithubIssue{}).
		Named("githubissue").
		Complete(r)
}
