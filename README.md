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

## OLMv1

The OLMv1 code is downstreamed into the three separate repositories:

* [operator-framework/catalogd](https://github.com/operator-framework/catalogd) -> [openshift/operator-framework-catalogd](https://github.com/openshift/operator-framework-catalogd)
* [operator-framework/operator-controller](https://github.com/operator-framework/operator-controller) -> [openshift/operator-framework-operator-controller](https://github.com/openshift/operator-framework-operator-controller) - the main repo
* [operator-framework/rukpak](https://github.com/operator-framework/rukpak) -> [openshift/operator-framework-rukpak](https://github.com/openshift/operator-framework-rukpak)

The upstream commits are downstreamed as a direct mirror (i.e. commit SHAs remain the same) and then merged into the `main` branch.

However `operator-controller` has dependencies on the `catalogd` and `rukpak` repos, and requires that its `go.mod` is updated to reflect the downstream (openshift) repositories.
1. Use the upstream `go.mod` file in `operator-controller` to determine which commits of `catalogd` and `rukpak` are to be merged.
2. Synchronize all three upstream repo to their respective downstream repos.
3. If there are no changes to `catalogd` or `rukpak` (i.e. they've been merged), update the downstream `go.mod` file in `operator-controller` to reference those downstream repos.

Only if there are no outstanding merges for downstream `catalogd` or `rukpak`, is the `go.mod` file in downstream `operator-controller` updated.

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

## Manual Merging

Running the merge tools manually will allow you to do the merging in a local repository to fix any issues that the tool itself cannot handle.

Running the tool without specifying the `-mode` option will just list the commits that need to be merged.

Running the tool with the `-mode=synchronize` will perform the actual merge in the local repository. A PR can then be generated from the results.

Running the tool with the `-mode=publish` will perform the merge in the local repository, and then attempt to publish the PR. However, if you don't have the correct credentials set, it will still print the contents of the PR description that can be used to manually create a PR.

### Cleanup

After merging, the tools may leave cruft behind in the local repository that may interfere with subsequent runs of the tool and/or other git operations.

```
git clean -fdx
git reflog expire --expire-unreachable=now --all
git gc --prune=now
git worktree prune
```
The periodic jobs are run in containers, and don't need any cleanup.

### Options/Flags

Options and flags may be found in the code:

* [Generic - applies to both](https://github.com/openshift/operator-framework-tooling/blob/main/pkg/flags/options.go#L76)
* [OLMv0 specific](https://github.com/openshift/operator-framework-tooling/blob/main/pkg/v0/cmd.go#L48)
* [OLMv1 specific](https://github.com/openshift/operator-framework-tooling/blob/main/pkg/v1/cmd.go#L43)
