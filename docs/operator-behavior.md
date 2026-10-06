# GithubIssue operator: behavior and design

The operator keeps a GitHub issue in sync with a `GithubIssue` custom resource (CR). Creating a CR opens an issue, editing the CR edits the issue, and deleting the CR closes the issue.

This document describes the agreed design, which is implemented. See [Implementation status](#implementation-status) at the end for what is left.

## The resource

```yaml
apiVersion: github.itayshviro.dev/v1alpha1
kind: GithubIssue
metadata:
  name: login-bug
  namespace: default
spec:
  repo: https://github.com/itay/foo    # validated by a Pattern in the CRD
  title: Login fails on Safari
  description: Steps to reproduce...   # may be empty
```

- `spec.repo` must match `^https://github\.com/<owner>/<repo>/?$`. The API server rejects anything else.
- `spec.repo` can't be changed after the CR is created, not even by a trailing slash or letter case. A CEL rule in the CRD (`self == oldSelf`) rejects it, because the annotation stores only the issue number: with another repo it would point to an unrelated issue. To use another repo, delete the CR and create a new one.
- `status.conditions` reports whether the CR is in sync with its issue, whether the issue is open, and whether a pull request is linked to it. See [Status conditions](#status-conditions).

## How a CR is linked to its issue

Each CR is linked to one issue by the annotation `github.itayshviro.dev/issue-number`. The controller sets it; users don't.

- A CR that has the annotation, where no older CR has the same repo and number, **manages** that issue. It is the working CR for it.
- The title is used to find an issue only once, the first time a CR is linked, and only among **open** issues.
- After that, the issue is fetched by number. This is what lets the CR's title change without losing the issue.

**Why an annotation and not a status field:** the annotation survives backup and restore and copying YAML between clusters, which often drop `status`. Normal `kubectl apply` does not remove it either:

- Client-side apply only deletes fields that were in the user's previously applied file. The controller added the annotation, so it was never in that file.
- Server-side apply tracks which manager owns each field. The controller owns the annotation, so a user's apply leaves it alone.

It is only lost through a deliberate `kubectl edit`, `kubectl annotate ...-`, or `kubectl replace`.

## Reconcile flow

1. Fetch the CR. If it no longer exists, stop.
2. If the CR is being deleted, run the [deletion flow](#deletion).
3. Make sure the CR has the finalizer `github.itayshviro.dev/finalizer`.
4. Work out which issue the CR manages:
   - **The CR has the annotation.**
     - If an older CR has the same repo and number, the annotation was copied (for example from an exported YAML). The controller removes it from this newer CR and continues as if it never had one.
     - Otherwise, fetch the issue by number.
       - If it was deleted (410) or moved (301) on GitHub, or GitHub can't find it (404), keep the annotation, report it in the status, and stop here. See [Issues deleted or moved on GitHub](#issues-deleted-or-moved-on-github).
     - An annotation that isn't a valid number is removed the same way as a copied one.
   - **The CR has no annotation.**
     - If another CR with the same repo and title manages an issue, this CR is a [duplicate](#duplicate-crs). Stop here.
     - If an older CR without the annotation has the same repo and title, that one goes first; this CR is a duplicate for now.
     - Otherwise, search the repo's **open** issues for the exact title, skipping issues other CRs already manage. Adopt the match, or create a new issue if there is none. Save the annotation **immediately**, before anything else can fail.
5. If the issue's title or description differs from the CR, update the issue. The CR is the source of truth for both fields. The issue's open/closed state is never changed here, so an issue closed on GitHub is not reopened.
6. Read the issue's timeline to see whether a pull request is linked to it. See [how "linked" is detected](#status-conditions).
7. Update the [status conditions](#status-conditions). The status is only written when something changed.
8. Requeue after 1 minute (the resync period).

**Why issues other CRs manage are skipped in the title search:** CR A manages #42 titled X, and a duplicate B also has title X. A user renames A to Y. If B reconciles before A has renamed #42 on GitHub, #42 is still an open issue titled X. Without the skip, B would adopt #42 and two CRs would manage it. With the skip, B creates a new issue titled X.

"Older" always means an earlier `metadata.creationTimestamp`, with the name as a tie-break. The API server sets the timestamp when an object is created and ignores any value in applied YAML, so a copied CR is always the newer one.

## Status conditions

Each condition has a `status` (`True`, `False` or `Unknown`), a `reason` code, a `message` for people, a `lastTransitionTime` that changes only when `status` changes, and the `observedGeneration` of the spec it refers to.

**`Ready`: is the CR in sync with its issue?**

| status | reason | When |
|---|---|---|
| True | `Synced` | The issue matches the CR. Message: `Managing GitHub issue #42` |
| False | `DuplicateIssue` | Another CR comes first for this repo and title; the message names it |
| False | `IssueDeleted` | The managed issue was deleted or moved on GitHub; the message says which |
| False | `IssueNotFound` | GitHub returns 404 for the managed issue |
| False | `ReconcileFailed` | Any other error; the message has the details, and the reconcile is retried with backoff |

**`IssueOpen`: is the GitHub issue open?**

| status | reason | When |
|---|---|---|
| True | `Open` | The issue is open |
| False | `Closed` | The issue is closed |
| False | `Deleted` | The issue was deleted or moved on GitHub |
| Unknown | `NotFound` | GitHub returns 404, so the state can't be known |
| (absent) | | The CR manages no issue, for example a duplicate |

**`IssueHasPR`: is a pull request linked to the issue?**

| status | reason | When |
|---|---|---|
| True | `PullRequestLinked` | A pull request is linked from the issue's "Development" sidebar. It stays True after the pull request is merged or closed |
| False | `NoPullRequest` | No pull request is linked, or every link was removed |
| Unknown | `Deleted` / `NotFound` | The issue was deleted or moved, or GitHub returns 404 |
| (absent) | | The CR manages no issue, for example a duplicate |

The three conditions are independent. For example, an issue closed by a merged pull request has `IssueOpen=False` and `IssueHasPR=True`.

**How "linked" is detected:** the issue's timeline records a `connected` event when a pull request is linked from the Development sidebar and a `disconnected` event when the link is removed. The operator counts `connected` minus `disconnected`; more than zero means linked. Merging or closing a pull request doesn't add either event, so the link stays. The REST API doesn't say which pull request an event refers to or what state it is in, so the message can't name the pull request or say whether it is open, merged or closed. A pull request that only mentions the issue (a `cross-referenced` event) doesn't count. Checked against the real API: a closing keyword such as `Fixes #42` in a pull request's description also creates only a `cross-referenced` event, so that pull request is **not** detected, even though GitHub lists it in the Development sidebar. See [Implementation status](#implementation-status).

## Duplicate CRs

At most one CR manages a given issue. A CR that would share an issue with another CR becomes a duplicate:

- It stays in the cluster.
- It makes no GitHub calls, so it never edits or closes the issue.
- Its status shows `Ready=False` with reason `DuplicateIssue` and a message naming the CR that manages the issue.
- It is checked again on every resync.

When the managing CR is deleted, its issue is closed. On its next resync, within a minute, the **oldest** duplicate with the same repo and title takes over. It creates a **new** issue, because the title search ignores the issue that was just closed.

Deleting a CR never deletes other CRs. The CRs have no owner relationship between them.

## Issues deleted or moved on GitHub

Repository admins can delete issues on GitHub, transfer them to another repository, or rename the repository. The operator does **not** recreate a deleted issue, and it doesn't follow a moved one:

- **Deleted (GitHub answers 410):** the CR keeps its annotation, so it keeps its claim on the title and other CRs with that title stay duplicates. The status shows `Ready=False/IssueDeleted`, `IssueOpen=False/Deleted` and `IssueHasPR=Unknown/Deleted`. No GitHub updates are attempted.
- **Moved (GitHub answers 301):** the issue was transferred to another repository, or the repository was renamed or transferred. A CR tracks its issue by repo and number, so this is treated like a deleted issue: the annotation is kept, the status is the same as above with a message that says the issue moved, and no GitHub updates are attempted. A CR that isn't linked yet reports `Ready=False/ReconcileFailed` instead, because its title search fails, and is retried with backoff.
- **Not found (GitHub answers 404):** the number doesn't exist in the repo, the repo is gone, or the token can't see it. This is often a token or access problem that gets fixed, so the annotation is kept here too. The status shows `Ready=False/IssueNotFound`, `IssueOpen=Unknown/NotFound` and `IssueHasPR=Unknown/NotFound`.

All three cases are rechecked every resync without error backoff. To give such a CR a new issue, remove its annotation (`kubectl annotate githubissue <name> github.itayshviro.dev/issue-number-`) or delete and recreate the CR.

After a repository is renamed, create new CRs with the new URL (`repo` can't be changed). Each one finds its still-open issue by title and adopts it. Deleting the old CRs doesn't close anything, since closing gets a 301 too.

**Why redirects aren't followed:** Go's HTTP client follows a 301 automatically but turns a `PATCH` into a `GET`, so an update or close would silently do nothing while looking successful. A followed `GET` would also return the moved issue with its number in the new repository, which the operator would then use with the old one.

## Deletion

- **The CR manages an issue** (it has the annotation, and no older CR has the same number): close the issue by number, then remove the finalizer.
  - Closing an issue that is already closed succeeds (GitHub answers 200).
  - If GitHub answers 404, 410 or 301 (moved), there is nothing to close and the finalizer is removed anyway.
- **The CR doesn't manage an issue** (a duplicate, or never linked): remove the finalizer only. The issue is not touched.
- If closing fails with any other error, the finalizer stays and the controller retries. The CR remains in `Terminating` until the close succeeds.

## Common scenarios

| What the user does | What happens |
|---|---|
| Edits `spec.title` in the YAML and runs `kubectl apply` | Same CR (same `metadata.name`), so the same issue is renamed on GitHub |
| Edits `spec.description` | The issue's description is updated |
| Applies a YAML with a new `metadata.name` and the same repo and title as an existing CR | The new CR is a duplicate until the existing CR is deleted, then it creates a new issue |
| Applies an exported YAML (with the annotation) under a new name | The copied annotation is removed from the new CR, which then follows the no-annotation rules |
| Renames a CR to a title another CR already uses | Allowed. GitHub allows two issues with the same title; they have different numbers |
| Edits the issue's title or description on GitHub | Reverted to the CR's values on the next resync |
| Closes the issue on GitHub | It stays closed; `IssueOpen=False/Closed` |
| Deletes the issue on GitHub | Not recreated; the CR keeps its annotation and reports `IssueDeleted` |
| Transfers the issue to another repo on GitHub | Treated like a deleted issue; the `IssueDeleted` message says it moved |
| Renames or transfers the repo on GitHub | Same as a transferred issue, for every CR of that repo. New CRs with the new URL adopt the open issues by title |
| Changes `spec.repo` | Rejected by the API server: `repo is immutable` |
| Links a pull request from the issue's Development sidebar | `IssueHasPR=True/PullRequestLinked` on the next resync |
| Opens a pull request with `Fixes #42` in its description | Not detected: `IssueHasPR` stays False (see [how "linked" is detected](#status-conditions)) |
| Merges or closes that pull request | `IssueHasPR` stays True; the link remains |
| Removes the link from the sidebar | `IssueHasPR=False/NoPullRequest` |
| Deletes a CR | Its issue is closed, if it manages one |

## GitHub API

- Calls go through [internal/controller/github/client.go](../internal/controller/github/client.go), a thin wrapper around the [go-github](https://github.com/google/go-github) library (v90, the newest release that supports Go 1.25). The wrapper adds the title search, the pull-request check and the error mapping below.
- The token comes from the `GITHUB_TOKEN` environment variable, which the Deployment fills from Secret `github-token-secret`, key `GITHUB_TOKEN`. The manager exits at startup if it is missing.
- go-github sends the API version header `X-GitHub-Api-Version: 2022-11-28` and doesn't let a client change it. GitHub supports that version until at least March 2028.
- A 404 is returned as `github.ErrNotFound`, a 410 as `github.ErrGone` and a 301 as `github.ErrMoved`, all wrapped with the repository or issue they refer to; check them with `errors.Is`. For an issue, 410 means it was deleted; for a repository, it means issues are disabled. Redirects are not followed (see [Issues deleted or moved on GitHub](#issues-deleted-or-moved-on-github)): go-github's default HTTP client follows them, so the wrapper passes its own. Other non-2xx responses become go-github errors that include GitHub's message.

| Operation | Endpoint |
|---|---|
| Find an open issue by title | `GET /repos/{owner}/{repo}/issues?state=open` (paged, pull requests skipped) |
| Get an issue | `GET /repos/{owner}/{repo}/issues/{number}` |
| Create an issue | `POST /repos/{owner}/{repo}/issues` |
| Update title and description | `PATCH /repos/{owner}/{repo}/issues/{number}` |
| Check for a linked pull request | `GET /repos/{owner}/{repo}/issues/{number}/timeline` (paged) |
| Close an issue | `PATCH /repos/{owner}/{repo}/issues/{number}` with `state: closed` |

**Difference from the assignment README:** the README says to search all of the repo's issues by title. This operator searches only open issues, so that a CR taking over after a deletion creates a new issue instead of adopting the closed one.

## Errors and retries

- When a reconcile returns an error, controller-runtime retries that CR with exponential backoff, from a few milliseconds up to about 16 minutes. Other CRs are not affected.
- A successful reconcile requeues after 1 minute. So do the duplicate, deleted and not-found cases, which are reported in the status rather than returned as errors.
- Errors that never clear, such as a revoked token, retry indefinitely at the maximum backoff until fixed.

## Guarantees and known limits

- **One issue, one managing CR.** The controller enforces this. Reconciles run one at a time (one worker, and leader election allows one active manager). When a CR claims an issue for the first time, the check for other CRs reads directly from the API server rather than from the controller's cache, so a claim saved a moment earlier is always visible.
- **No admission webhook.** Duplicates are accepted by `kubectl apply` and reported in the status, not rejected. A validating webhook could be added later for immediate feedback; the controller check would remain the actual guarantee.
- **The window between creating an issue and saving the annotation.** If saving the annotation fails, the next reconcile finds the issue by title and saves it then. If the CR is deleted inside that window, the issue stays open on GitHub.
- **A stale copy of the CR itself.** The check for other CRs reads from the API server, but the CR being reconciled comes from the controller's cache. If the cache still lacks an annotation saved a moment earlier (it would have to lag by more than one GitHub request), a retry searches by title again. If GitHub's list doesn't show the new issue yet, it creates a second one; saving that number then fails with a conflict, so the second issue stays open and unmanaged. A fix is proposed but not applied: re-read the CR from the API server just before linking.
- **Rate limits.** A token allows 5,000 requests per hour. Each linked CR costs about two requests per minute (the issue and its timeline, more if the timeline has over 100 events), which limits one token to roughly 40 CRs. Conditional requests with ETags or a longer resync period would raise that.

## Decisions

- Duplicate detection is cluster-wide, because an issue is global and CRs in two namespaces could otherwise share one.
- "Has a pull request" means linked from the issue's Development sidebar, detected with the REST timeline API. GraphQL was not used.

## Implementation status

- **Done:**
  - The GitHub client on top of go-github (find open issue by title, get, create, update, close, check the timeline for a linked pull request).
  - Constants in [internal/controller/utils/consts.go](../internal/controller/utils/consts.go).
  - Shared helpers in [internal/controller/utils/utils.go](../internal/controller/utils/utils.go), including the copied-annotation check.
  - Finalizer helpers.
  - `handleDelete` and `handleUpdate`, following the rules above, including the `Ready`, `IssueOpen` and `IssueHasPR` conditions and the 1-minute requeue.
  - Tests, none of which call the real GitHub:
    - [githubissue_controller_test.go](../internal/controller/githubissue_controller_test.go): the four unit tests the assignment requires (create when missing, create fails, update fails, close on delete), plus deleted, moved and not-found issues and duplicate CRs. They run on envtest against an in-memory fake GitHub server.
    - [client_test.go](../internal/controller/github/client_test.go): the GitHub client's requests, paging, error mapping and pull-request counting.
    - [utils_test.go](../internal/controller/utils/utils_test.go): the linking rules: copied annotations, title claims and ordering by age.
- **Open:**
  - Pull requests linked with a closing keyword aren't detected (see [how "linked" is detected](#status-conditions)). A REST-only fix was proposed: count `cross-referenced` events from pull requests whose current description has a closing keyword for the issue. It is on hold.
  - The stale copy of the CR described in [Guarantees and known limits](#guarantees-and-known-limits).
