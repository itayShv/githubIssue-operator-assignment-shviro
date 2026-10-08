# Home Assignment1

Implement an operator that will allow creating/editing a github issue.

**Please make sure you have a basic understanding of the following concepts before you continue to read.**
- [Controller](https://kubernetes.io/docs/concepts/architecture/controller/) 
- [Custom Resource Definition (CRD)](https://kubernetes.io/docs/concepts/extend-kubernetes/api-extension/custom-resources/)
- [Operator](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/) 
- [Kubebuilder](https://book.kubebuilder.io)
- [Operator-SDK](https://sdk.operatorframework.io/docs/)

## GithubIssue Operator

### Spec and Status

The kubernetes GithubIssue object must have those fields in its spec:

- `repo`: to represent the url to the github repo - e.g https://github.com/rgolangh/dotfiles
- `title`: the title of the issue
- `description`: used as the description of the issue

The status field of the object must contain this fields:

- `conditions` (`[ ]metav1.Condition`): for each condition make sure to have `lastTransitionTimestamp`. Follow those guidelines to describe those conditions:
    - issue has a PR
    - issue is open

### The Reconciliation behaviour:
- Fetch the object
- Fetch all the github issues of the repository. Find one with the exact title you need:
    - If it doesn't exist -> create a github issue with the title and description.
    - If it exists  -> update the description (if needed).
- Update the k8s status with the real github issue state.

### Things to address
-  Implement deletion behaviour: a delete of the k8s object, triggers closing the github issue.
-  Tweak the resync period to every 1 minute.
-  Store the GitHub token you use in a secret and use it in the code by reading an env variable.
-  Add validation in the CRD level - an attempt to create a CRD with malformed 'repo' will fail
-  Writing unit tests. Those test cases should pass and cover:
    - Failed attempt to create a real github issue
    - Failed attempt to update an issue
    - Create if issue not exist
    - Close issues on delete

## Tools you should use
This repo contains a go project you can fork it and use it as a template, also you will need:
- [GitHub API Interaction](https://docs.github.com/en/rest/guides/getting-started-with-the-rest-api#issues) for communicating with the GitHub API
- [Kind](https://kind.sigs.k8s.io)  for creating local cluster
- [Go](https://go.dev) your operator should be written in Go
- [Kubebuilder](https://book.kubebuilder.io) for creating the operator and crd template
- [Operator-SDK](https://sdk.operatorframework.io/docs/) for documentation about controllers and syntax
- [Ginkgo](https://onsi.github.io/ginkgo/) for testing