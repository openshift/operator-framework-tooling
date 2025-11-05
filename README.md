# operator-framework-tooling

This repository contains tools for downstreaming OLM software

## OLMv0

The OLMv0 code is downstreamed into the [openshift/operator-framework-olm](https://github.com/openshift/operator-framework-olm) repository from the following upstream repositories:

* [operator-framework/api](https://github.com/operator-framework/api)
* [operator-framework/operator-lifecycle-manager](https://github.com/operator-framework/operator-lifecycle-manager) - the main repo
* [operator-framework/operator-registry](https://github.com/operator-framework/operator-registry)

The `operator-lifecycle-manager` repository is the main repository, as it refers to the others. Its `go.mod` file determines what versions of `api` and `operator-registry` are used and downstreamed. Any integration between these three repositories _must_ be done upstream, as that will provide a clean downstreaming experience.

The upstream repositories must also be `go mod tidy` clean.

The downstream repository is a "monorepo" consisting of all the above repositories each located in a `staging` directory. The upstream commits are cherry-picked with updating comments indicating the source repository and commit SHA. 

In the monorepo there are individual Dockerfiles controlling downstream builds for each tool. For instance: https://github.com/openshift/operator-framework-olm/blob/master/operator-lifecycle-manager.Dockerfile

## OLMv1

The OLMv1 code is downstreamed into one repository:

* [operator-framework/operator-controller](https://github.com/operator-framework/operator-controller) -> [openshift/operator-framework-operator-controller](https://github.com/openshift/operator-framework-operator-controller)

The upstream commits are downstreamed as a direct mirror (i.e. commit SHAs remain the same) and then merged into the `main` branch.

The prior requirement of the **operator-framework/rukpak** and **operator-framework/catalogd** repositories is no longer necessary.

### Merging Downstream

Merging to downstream consists of:
1. Merging via merge commit upstream `main` branch, this overrides the existing `main` branch via `git merge --stategy=ours`. This keeps the upstream and downstream commits numbered with the same SHA.
2. Then cherry-pick commits as needed:
  * Dropping commits that have the headline: `UPSTREAM: <drop>:`
  * Keeping commits that have the headline: `UPSTREAM: <carry>:`
  * Determine if a cherry-pick needs to be kept `UPSTREAM: 1234:` by looking at what's being merged.
3. Finally, do any post `go mod` and `make manifests`-type processing.

## Building

Just run `make` or `make build`. Two executables will be created in the root of the repository

* `v0` - for downstreaming OLMv0
* `v1` - for downstreaming OLMv1

## Periodic Jobs

This tool is intended to be run as a periodic job with minimal human interaction.

* [OLMv0 periodic job](https://prow.ci.openshift.org/?job=periodic-auto-olm-downstreaming)
* [OLMv1 periodic job](https://prow.ci.openshift.org/?job=periodic-auto-olm-v1-downstreaming)

The jobs are defined in:

* [infra-periodics.yaml](https://github.com/openshift/release/blob/master/ci-operator/jobs/infra-periodics.yaml)

## Post-Submit Job

This tool is also intended to be run as a post-submit job with minimal human interaction. This updates any outstanding upstream sync PRs.

* [OLMv1 post-submit job](https://prow.ci.openshift.org/?job=post-ci-openshift-operator-framework-operator-controller-main-refresh-bumper-pr)

The job is defined in:

* [openshift-operator-framework-operator-controller-main-postsubmits.yaml](https://github.com/openshift/release/blob/master/ci-operator/jobs/openshift/operator-framework-operator-controller/openshift-operator-framework-operator-controller-main-postsubmits.yaml)

## Manual Merging

Running the merge tools manually will allow you to do the merging in a local repository to fix any issues that the tool itself cannot handle.

Running the tool without specifying the `-mode` option will just list the commits that need to be merged.

Running the tool with the `-mode=synchronize` will perform the actual merge in the local repository. There are a number of `pause` options that can be added to allow you to fix the sync while it runs. A PR can then be generated from the results.

Running the tool with the `-mode=publish` will perform the merge in the local repository, and then attempt to publish the PR. However, if you don't have the correct credentials set, it will still print the contents of the PR description that can be used to manually create a PR.

See the [examples](https://github.com/openshift/operator-framework-tooling/tree/main/examples) directory for a set of sample scripts.

### Cleanup

After merging, the tools may leave cruft behind in the local repository that may interfere with subsequent runs of the tool and/or other git operations.

```
git clean -fdx
git reflog expire --expire-unreachable=now --all
git gc --prune=now
git worktree prune
```
The periodic jobs are run in containers, and don't need any cleanup. You may also want to consider running the tool in a clean set of repositories.

### Options/Flags

Options and flags may be found in the code:

* [Generic - applies to both](https://github.com/search?q=repo%3Aopenshift%2Foperator-framework-tooling+path%3Apkg%2Fflags%2F*.go+%2Ffs%5C..%2BVar%2F&type=code)
* [OLMv0 specific](https://github.com/search?q=repo%3Aopenshift%2Foperator-framework-tooling+path%3Apkg%2Fv0%2F*.go+%2Ffs%5C..%2BVar%2F&type=code)
* [OLMv1 specific](https://github.com/search?q=repo%3Aopenshift%2Foperator-framework-tooling+path%3Apkg%2Fv1%2F*.go+%2Ffs%5C..%2BVar%2F&type=code)

## Updating golang version

This tool runs in a container based on a given OCP release with a specific golang version. Periodically, this will need to be updated to handle upstream updates. The process is as follows:

1. Update [openshift/release/.../openshift-operator-framework-tooling-main.yaml](https://github.com/openshift/release/blob/master/ci-operator/config/openshift/operator-framework-tooling/openshift-operator-framework-tooling-main.yaml) to the correct OCP/golang version. Example: https://github.com/openshift/release/pull/60676
2. Update the Dockerfiles here. Example: https://github.com/openshift/operator-framework-tooling/pull/63
