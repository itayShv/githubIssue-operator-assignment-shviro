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
	"net/http"

	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	githubv1alpha1 "github.com/itayshviro/githubissue-operator/api/v1alpha1"
	"github.com/itayshviro/githubissue-operator/internal/controller/finalizer"
	"github.com/itayshviro/githubissue-operator/internal/controller/utils"
)

// GithubIssueReconciler reconciles a GithubIssue object
type GithubIssueReconciler struct {
	client.Client
	Scheme *runtime.Scheme
	GitHubToken string
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
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !gitHubIssueCR.ObjectMeta.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &gitHubIssueCR)
	}

	if err := finalizer.Ensure(ctx, &gitHubIssueCR, r.Client); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure finalizer in GitHub issue CR: %s", err.Error())
	}

	return r.handleUpdate(ctx, &gitHubIssueCR)
}

func (r *GithubIssueReconciler) handleDelete(ctx context.Context, cr *githubv1alpha1.GithubIssue) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	logger.Info("Starting Delete CR event of the github issue " + cr.Name)

	if controllerutil.ContainsFinalizer(cr, utils.GitHubIssueDeletionFinalizer) {
		//logic
		if http.Response.StatusCode == http.StatusOK {
			logger.Info("Successfully closed GitHub issue")
		} else if resp.StatusCode == http.StatusNotFound {
			logger.Info("GitHub issue wasnt found - no deleation needed")
		} else {
			err := fmt.Errorf("github api returned unexpected status: %d", resp.StatusCode)
			logger.Error(err, "Failed to close issue in GitHub")
			return ctrl.Result{}, err
		}
		// remove finalizer to allow Kubernetes to permanently delete the CR
		logger.Info("Removing Finalizer")
		if err := finalizer.Remove(ctx, cr, r.Client); err != nil {
			return ctrl.Result{}, err
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *GithubIssueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&githubv1alpha1.GithubIssue{}).
		Named("githubissue").
		Complete(r)
}
