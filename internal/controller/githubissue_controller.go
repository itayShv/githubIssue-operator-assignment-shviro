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
	"errors"
	"fmt"
	"strconv"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
			// Normal right after a delete: the finalizer was removed and the CR is gone
			logger.Info("GithubIssue was deleted, nothing to do")
		}
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !gitHubIssueCR.DeletionTimestamp.IsZero() {
		return r.handleDelete(ctx, &gitHubIssueCR)
	}

	if err := finalizer.Ensure(ctx, &gitHubIssueCR, r.Client); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to ensure finalizer in GitHub issue CR: %s", err.Error())
	}

	return r.handleUpdate(ctx, &gitHubIssueCR)
}

func (r *GithubIssueReconciler) handleDelete(ctx context.Context, cr *githubv1alpha1.GithubIssue) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)

	if !controllerutil.ContainsFinalizer(cr, utils.GitHubIssueDeletionFinalizer) {
		return ctrl.Result{}, nil
	}

	number, ok, err := utils.ManagedIssueNumber(ctx, r.Client, cr)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !ok {
		logger.Info("GithubIssue does not manage a GitHub issue, skipping issue close", "name", cr.Name)
	} else {
		owner, repo, err := utils.ParseRepoURL(cr.Spec.Repo)
		if err != nil {
			return ctrl.Result{}, err
		}
		gh, err := r.githubClient()
		if err != nil {
			return ctrl.Result{}, err
		}
		err = gh.CloseIssue(ctx, owner, repo, number)
		switch {
		case errors.Is(err, github.ErrNotFound), errors.Is(err, github.ErrGone), errors.Is(err, github.ErrMoved):
			logger.Info("GitHub issue was not found, deleted or moved, nothing to close", "error", err.Error())
		case err != nil:
			logger.Error(err, "Failed to close GitHub issue", "number", number)
			return ctrl.Result{}, err
		default:
			logger.Info("Closed GitHub issue", "number", number)
		}
	}

	// Remove the finalizer so Kubernetes can delete the CR
	if err := finalizer.Remove(ctx, cr, r.Client); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("Removed finalizer from GithubIssue", "name", cr.Name)
	return ctrl.Result{}, nil
}

func (r *GithubIssueReconciler) handleUpdate(ctx context.Context, cr *githubv1alpha1.GithubIssue) (ctrl.Result, error) {
	logger := logf.FromContext(ctx)
	// handle create or edit event
	logger.Info("Starting Update procedure for GithubIssue", "name", cr.Name, "repo", cr.Spec.Repo, "title", cr.Spec.Title)
	defer logger.Info("Finish Update procedure for GithubIssue", "name", cr.Name, "repo", cr.Spec.Repo, "title", cr.Spec.Title)

	owner, repo, err := utils.ParseRepoURL(cr.Spec.Repo)
	if err != nil {
		return r.fail(ctx, cr, err)
	}
	gh, err := r.githubClient()
	if err != nil {
		return r.fail(ctx, cr, err)
	}

	// Find the issue this CR manages
	number, linked, err := utils.ManagedIssueNumber(ctx, r.Client, cr)
	if err != nil {
		return r.fail(ctx, cr, err)
	}
	var issue *github.Issue
	if linked {
		issue, err = gh.GetIssue(ctx, owner, repo, number)
		switch {
		case errors.Is(err, github.ErrGone), errors.Is(err, github.ErrMoved):
			// Keep the annotation, so the CR keeps its claim on the title and no other CR recreates the issue.
			// A moved issue is treated as deleted, since the CR tracks its issue by repo and number.
			logger.Info("GitHub issue was deleted or moved", "number", number, "error", err.Error())
			return r.setIssueMissingStatus(ctx, cr, number, err)
		case errors.Is(err, github.ErrNotFound):
			// Often a token or access problem, so keep the annotation and report it
			logger.Info("GitHub issue was not found", "number", number, "error", err.Error())
			return r.setIssueMissingStatus(ctx, cr, number, err)
		case err != nil:
			return r.fail(ctx, cr, err)
		}
	} else {
		// A copied or invalid annotation doesn't count: remove it, so the CR links by title like a new one
		if err := r.removeIssueAnnotation(ctx, cr); err != nil {
			return r.fail(ctx, cr, err)
		}
		// if issue dont exist -> find an open one with this title or create it, unless another CR comes first
		var blocker *githubv1alpha1.GithubIssue
		issue, blocker, err = r.linkIssue(ctx, cr, gh, owner, repo)
		if err != nil {
			return r.fail(ctx, cr, err)
		}
		if blocker != nil {
			logger.Info("GithubIssue is a duplicate, not managing a GitHub issue", "name", cr.Name, "managedBy", blocker.Namespace+"/"+blocker.Name)
			return r.setDuplicateStatus(ctx, cr, blocker)
		}
	}

	// if the CR or the issue changed, patch the github issue so it matches the CR
	if issue.GetTitle() != cr.Spec.Title || issue.GetBody() != cr.Spec.Description {
		issue, err = gh.UpdateIssue(ctx, owner, repo, issue.GetNumber(), cr.Spec.Title, cr.Spec.Description)
		if err != nil {
			return r.fail(ctx, cr, err)
		}
		logger.Info("Updated GitHub issue", "number", issue.GetNumber())
	}

	// issue has a PR
	hasPR, err := gh.HasLinkedPullRequest(ctx, owner, repo, issue.GetNumber())
	if err != nil {
		return r.fail(ctx, cr, err)
	}

	return r.setSyncedStatus(ctx, cr, issue, hasPR)
}

// removeIssueAnnotation removes the issue number annotation from the CR, if it has one.
func (r *GithubIssueReconciler) removeIssueAnnotation(ctx context.Context, cr *githubv1alpha1.GithubIssue) error {
	if _, found := cr.Annotations[utils.IssueNumberAnnotation]; !found {
		return nil
	}
	delete(cr.Annotations, utils.IssueNumberAnnotation)
	if err := r.Update(ctx, cr); err != nil {
		return err
	}
	logf.FromContext(ctx).Info("Removed issue number annotation from GithubIssue", "name", cr.Name)
	return nil
}

// linkIssue finds an open issue with the CR's title, or creates one, and saves its number on the CR.
// If another CR comes first for this repo and title, it returns that CR instead and changes nothing.
func (r *GithubIssueReconciler) linkIssue(ctx context.Context, cr *githubv1alpha1.GithubIssue, gh *github.Client, owner, repo string) (*github.Issue, *githubv1alpha1.GithubIssue, error) {
	logger := logf.FromContext(ctx)

	blocker, claimed, err := utils.CheckTitleClaim(ctx, r.apiReader(), cr)
	if err != nil {
		return nil, nil, err
	}
	if blocker != nil {
		return nil, blocker, nil
	}

	// Skip issues other CRs already manage(a CR uses its issue number), e.g. one whose CR was just renamed but not yet reconciled
	issue, err := gh.FindIssueByTitle(ctx, owner, repo, cr.Spec.Title, claimed)
	if err != nil {
		return nil, nil, err
	}
	if issue == nil {
		issue, err = gh.CreateIssue(ctx, owner, repo, cr.Spec.Title, cr.Spec.Description)
		if err != nil {
			return nil, nil, err
		}
		logger.Info("Created GitHub issue", "number", issue.GetNumber())
	} else {
		logger.Info("Found open GitHub issue with the same title", "number", issue.GetNumber())
	}

	// Save the link right away, before anything else can fail
	metav1.SetMetaDataAnnotation(&cr.ObjectMeta, utils.IssueNumberAnnotation, strconv.Itoa(issue.GetNumber()))
	if err := r.Update(ctx, cr); err != nil {
		return nil, nil, err
	}
	logger.Info("Saved issue number annotation on GithubIssue", "name", cr.Name, "number", issue.GetNumber())
	return issue, nil, nil
}

// condition builds a status condition for the CR's current generation.
func condition(cr *githubv1alpha1.GithubIssue, condType string, status metav1.ConditionStatus, reason, message string) metav1.Condition {
	return metav1.Condition{Type: condType, Status: status, Reason: reason, Message: message, ObservedGeneration: cr.Generation}
}

// pullRequestCondition reports whether a pull request is linked to the issue.
func pullRequestCondition(cr *githubv1alpha1.GithubIssue, linked bool) metav1.Condition {
	if linked {
		return condition(cr, utils.ConditionIssueHasPR, metav1.ConditionTrue, utils.ReasonPullRequestLinked, "A pull request is linked to the issue")
	}
	return condition(cr, utils.ConditionIssueHasPR, metav1.ConditionFalse, utils.ReasonNoPullRequest, "No pull request is linked to the issue")
}

// setSyncedStatus reports a CR in sync with its issue: Ready is True, IssueOpen follows the issue's state,
// and IssueHasPR says whether a pull request is linked.
func (r *GithubIssueReconciler) setSyncedStatus(ctx context.Context, cr *githubv1alpha1.GithubIssue, issue *github.Issue, hasPR bool) (ctrl.Result, error) {
	number := issue.GetNumber()
	ready := condition(cr, utils.ConditionReady, metav1.ConditionTrue, utils.ReasonSynced, fmt.Sprintf("Managing GitHub issue #%d", number))
	open := condition(cr, utils.ConditionIssueOpen, metav1.ConditionTrue, utils.ReasonOpen, fmt.Sprintf("GitHub issue #%d is open", number))
	if issue.GetState() == utils.GitHubIssueStateClosed {
		open = condition(cr, utils.ConditionIssueOpen, metav1.ConditionFalse, utils.ReasonClosed, fmt.Sprintf("GitHub issue #%d is closed", number))
	}
	return r.setStatus(ctx, cr, ready, open, pullRequestCondition(cr, hasPR))
}

// setDuplicateStatus reports a CR that waits because blocker comes first for its repo and title.
// It manages no issue, so only Ready is set.
func (r *GithubIssueReconciler) setDuplicateStatus(ctx context.Context, cr, blocker *githubv1alpha1.GithubIssue) (ctrl.Result, error) {
	msg := fmt.Sprintf("GithubIssue %s/%s comes first for this repo and title", blocker.Namespace, blocker.Name)
	return r.setStatus(ctx, cr, condition(cr, utils.ConditionReady, metav1.ConditionFalse, utils.ReasonDuplicateIssue, msg))
}

// setIssueMissingStatus reports a managed issue that GitHub can't return: deleted or moved (410, 301),
// or not found (404).
func (r *GithubIssueReconciler) setIssueMissingStatus(ctx context.Context, cr *githubv1alpha1.GithubIssue, number int, err error) (ctrl.Result, error) {
	readyReason, reason, open := utils.ReasonIssueDeleted, utils.ReasonDeleted, metav1.ConditionFalse
	msg := fmt.Sprintf("GitHub issue #%d was deleted", number)
	switch {
	case errors.Is(err, github.ErrMoved):
		msg = fmt.Sprintf("GitHub issue #%d was moved (transferred to another repository, "+
			"or its repository was renamed or transferred)", number)
	case errors.Is(err, github.ErrNotFound):
		readyReason, reason, open = utils.ReasonIssueNotFound, utils.ReasonNotFound, metav1.ConditionUnknown
		msg = err.Error()
	}
	return r.setStatus(ctx, cr,
		condition(cr, utils.ConditionReady, metav1.ConditionFalse, readyReason, msg),
		condition(cr, utils.ConditionIssueOpen, open, reason, msg),
		condition(cr, utils.ConditionIssueHasPR, metav1.ConditionUnknown, reason, msg))
}

// setStatus sets the Ready condition and the issue conditions (IssueOpen, IssueHasPR), saves the status
// if anything changed, and requeues after the resync period. With no issue conditions the CR manages
// no issue, so they are removed.
func (r *GithubIssueReconciler) setStatus(ctx context.Context, cr *githubv1alpha1.GithubIssue, ready metav1.Condition, issueConditions ...metav1.Condition) (ctrl.Result, error) {
	changed := meta.SetStatusCondition(&cr.Status.Conditions, ready)
	if len(issueConditions) == 0 {
		// The CR manages no issue: remove the conditions left from an issue it managed before
		for _, condType := range []string{utils.ConditionIssueOpen, utils.ConditionIssueHasPR} {
			changed = meta.RemoveStatusCondition(&cr.Status.Conditions, condType) || changed
		}
	} else {
		for _, c := range issueConditions {
			changed = meta.SetStatusCondition(&cr.Status.Conditions, c) || changed
		}
	}

	if changed {
		if err := r.Status().Update(ctx, cr); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: utils.ResyncPeriod}, nil
}

// fail records err in the Ready condition and returns it, so the reconcile is retried with backoff.
func (r *GithubIssueReconciler) fail(ctx context.Context, cr *githubv1alpha1.GithubIssue, err error) (ctrl.Result, error) {
	ready := condition(cr, utils.ConditionReady, metav1.ConditionFalse, utils.ReasonReconcileFailed, err.Error())
	if meta.SetStatusCondition(&cr.Status.Conditions, ready) {
		if statusErr := r.Status().Update(ctx, cr); statusErr != nil {
			logf.FromContext(ctx).Error(statusErr, "Failed to update GithubIssue status", "name", cr.Name)
		}
	}
	return ctrl.Result{}, err
}

func (r *GithubIssueReconciler) apiReader() client.Reader {
	if r.APIReader != nil {
		return r.APIReader
	}
	return r.Client
}

func (r *GithubIssueReconciler) githubClient() (*github.Client, error) {
	return github.NewClient(r.GitHubAPIURL, r.GitHubToken)
}

// SetupWithManager sets up the controller with the Manager.
func (r *GithubIssueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&githubv1alpha1.GithubIssue{}).
		Named("githubissue").
		Complete(r)
}
