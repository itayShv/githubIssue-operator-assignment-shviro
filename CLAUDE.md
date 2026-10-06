# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Read [AGENTS.md](AGENTS.md) first.** It holds the Kubebuilder rules for this repo: which files are generated and must not be hand-edited, the scaffold markers you must keep, the CLI scaffolding commands, logging style, and controller conventions. This file adds only what is specific to this project.

## What this is

A Kubebuilder (go/v4, CLI 4.14) operator for a home assignment ([README.md](README.md)). A namespaced `GithubIssue` CR (`github.itayshviro.dev/v1alpha1`) is kept in sync with a real GitHub issue. Go module: `github.com/itayshviro/githubissue-operator`.

What the assignment requires (per README):
- Spec: `repo` (a full `https://github.com/<owner>/<repo>` URL), `title`, `description`.
- Status: `conditions []metav1.Condition`, covering whether the issue is open and whether it has a PR.
- Reconcile: list the repo's issues and match on the **exact title**. If there is no match, create the issue. If there is a match, update the description when it differs. Then write the real issue state into the status conditions.
- On deletion, close the GitHub issue. This uses the finalizer `github.itayshviro.dev/finalizer`.
- Resync every 1 minute (`RequeueAfter` and/or the manager's `SyncPeriod`).
- The token comes from the `GITHUB_TOKEN` env var. In-cluster, it is filled from Secret `github-token-secret` key `GITHUB_TOKEN` ([config/manager/manager.yaml](config/manager/manager.yaml)). [cmd/main.go](cmd/main.go) exits if the variable is unset and passes the token to the reconciler as `GitHubToken`.
- CRD-level validation of `repo` is done with a `Pattern` marker in [api/v1alpha1/githubissue_types.go](api/v1alpha1/githubissue_types.go).
- The unit tests must cover: failing to create an issue, failing to update an issue, creating when the issue doesn't exist, and closing on delete. GitHub calls need to be mockable for this (for example an injectable HTTP client/base URL or an interface). Tests should not hit real GitHub.

## Operator design

**Read [docs/operator-behavior.md](docs/operator-behavior.md) before changing reconcile logic.** It is the design agreed with the user, including the status conditions (`Ready`, `IssueOpen`, `IssueHasPR`) and their reasons. Keep it in sync when behavior changes. The rules that are easy to break:

- A CR is linked to its issue by the annotation `github.itayshviro.dev/issue-number`, not by title. The title is used only to find an issue the first time, and only among **open** issues (a deliberate difference from the README).
- At most one CR manages an issue. The older CR (by `creationTimestamp`, then name) wins; the other is a duplicate that makes no GitHub calls and reports `Ready=False`, reason `DuplicateIssue`.
- Deleting a CR without the annotation removes the finalizer only and never touches an issue.
- Updates send the title and description; the issue's open/closed state is never changed except by closing on delete.
- An issue deleted (410), moved (301: transferred, or its repo renamed or transferred) or not found (404) on GitHub is never recreated or followed: the annotation is kept and the status reports it. GitHub returns 410, not 404, for a deleted issue.
- `spec.repo` is immutable (CEL rule `self == oldSelf` in the CRD), because the annotation stores only the issue number.
- "Has a PR" means linked from the issue's Development sidebar: timeline `connected` minus `disconnected` events. The user chose REST over GraphQL; don't switch without asking. Checked on the real API: a `Fixes #N` closing keyword in a PR description creates only a `cross-referenced` event, so it is **not** detected. A REST-only fix (count `cross-referenced` events from PRs whose current description has a closing keyword for the issue) was proposed; the user put it on hold.

## Code layout beyond the scaffold

- [internal/controller/githubissue_controller.go](internal/controller/githubissue_controller.go): `Reconcile` fetches the CR. If `DeletionTimestamp` is set it calls `handleDelete`; otherwise it calls `finalizer.Ensure` and then `handleUpdate`. `handleUpdate` finds the managed issue with `utils.ManagedIssueNumber`. If the CR isn't linked, it removes a copied or invalid annotation (`removeIssueAnnotation`) and links by title (`linkIssue`). The status is written by `setSyncedStatus`, `setDuplicateStatus` and `setIssueMissingStatus` (one per `Ready` outcome, all through `setStatus`, which writes only when a condition changed) and by `fail` (sets `ReconcileFailed` and returns the error, so the reconcile is retried with backoff). Reconciler fields: `GitHubAPIURL` overrides the GitHub base URL so tests can use an `httptest` server; `APIReader` (wired to `mgr.GetAPIReader()` in `cmd/main.go`) is an uncached reader used for the duplicate check when a CR claims an issue, falling back to `Client` when nil.
- [internal/controller/github/client.go](internal/controller/github/client.go): thin wrapper around [go-github](https://github.com/google/go-github) v90, imported as `gogithub` (v91+ need Go 1.26). It passes its own `http.Client` that never follows redirects, because go-github's default one does. A 404 is returned as `ErrNotFound`, a 410 as `ErrGone` and a 301 as `ErrMoved`, wrapped with the repo or issue; check them with `errors.Is`. `Issue` is an alias for go-github's issue type; read its fields with the getters (`GetNumber`, `GetTitle`...). go-github sends API version `2022-11-28` and has no option to change it. It imports `utils` for constants, so `utils` must never import `github` (import cycle).
- [internal/controller/finalizer/](internal/controller/finalizer/): `Ensure` and `Remove` helpers for the finalizer.
- [internal/controller/utils/consts.go](internal/controller/utils/consts.go): shared constants, in commented groups: the names set on CRs (finalizer, issue-number annotation), GitHub API settings (timeout, page size), values read from GitHub's responses (closed state, timeline event names), `ResyncPeriod`, and the condition types and reasons.
- [internal/controller/utils/utils.go](internal/controller/utils/utils.go): helpers shared by the reconcile paths: `ParseRepoURL`, `SameRepo` (case-insensitive owner/repo), `ManagedIssueNumber` (reads the annotation and applies the older-CR-wins rule, listing CRs cluster-wide), `CheckTitleClaim` (duplicate check before linking by title), and `IsOlder`.

**Current state:** the reconcile logic is complete and all tests pass. The tests use Ginkgo, and none call the real GitHub:
- [githubissue_controller_test.go](internal/controller/githubissue_controller_test.go): envtest plus `fakeGitHub`, an in-memory GitHub served by `httptest` (`makeUnavailable` makes it answer 410, 301 or 404 for an issue). It covers the four README cases, plus deleted, moved and not-found issues and duplicate CRs.
- [github/client_test.go](internal/controller/github/client_test.go): an `httptest` server that records each request. It covers the token, request bodies, paging (the server sends `Link` headers, which go-github follows), error mapping, redirects not being followed, and pull-request counting.
- [utils/utils_test.go](internal/controller/utils/utils_test.go): controller-runtime's fake client. It doesn't assign UIDs, and the helpers skip the CR itself by UID, so test CRs set `UID` explicitly.

Open items:
- Closing keywords (`Fixes #N`) aren't detected by `IssueHasPR` (see Operator design above).
- The CR being reconciled is read from the cache. If the cache lags after the annotation is saved, a retry can search by title again and create a second issue. Proposed fix, not applied: at the start of `linkIssue`, re-read the CR through `apiReader()` and stop if it already has the annotation.
- The e2e test doesn't create `github-token-secret`, so the manager pod can't start in the e2e cluster.
- [config/samples/github_v1alpha1_githubissue.yaml](config/samples/github_v1alpha1_githubissue.yaml) still has an empty spec.

## Commands

```bash
make manifests generate   # after editing *_types.go or kubebuilder markers
make build                # bin/manager
make lint / make lint-fix # golangci-lint (custom build config in .custom-gcl.yml)
make test                 # envtest-based unit tests (excludes test/e2e)
make run                  # run locally against the current kubeconfig; needs GITHUB_TOKEN exported
make test-e2e             # creates Kind cluster "githubissue-operator-assignment-shviro-test-e2e", runs, and deletes it
```

Run a single test or package. `make test` or `make setup-envtest` must have run once first so the envtest binaries exist in `bin/`:

```bash
KUBEBUILDER_ASSETS="$(bin/setup-envtest-* use -p path --bin-dir bin)" \
  go test ./internal/controller/... -ginkgo.focus="should close the GitHub issue"
```

`make lint` can't load packages under Go 1.27 (golangci-lint v2.11.4). The dev container pins Go 1.25 like `go.mod` and CI; with a newer local Go, run `GOTOOLCHAIN=go1.25.7 make lint`.

`make run` needs the CRD installed first (`make install`) and `GITHUB_TOKEN` exported in the same shell. The local Kind cluster is `github-issue-operator`; if `~/.kube/config` is missing, recreate it with `kind export kubeconfig --name github-issue-operator`. The test repo `itayShv/githubIssue-operator-assignment-shviro` is a fork, and forks start with Issues disabled (GitHub answers 410), so Issues had to be turned on in its settings.

To deploy to Kind, create the token secret in the operator namespace before running `make deploy`:

```bash
kubectl create secret generic github-token-secret -n githubissue-operator-assignment-shviro-system --from-literal=GITHUB_TOKEN=<token>
```

CI ([.github/workflows/](.github/workflows/)) runs lint, `make test`, and e2e.
