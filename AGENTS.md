# AI Agent Guide for operator-framework-tooling

This document provides guidance for AI coding agents (like Claude Code, GitHub Copilot, etc.) working with this repository.

## Repository Overview

This repository contains tools for downstreaming OLM (Operator Lifecycle Manager) software from upstream repositories to OpenShift downstream repositories. It provides two main tools:

- **v0**: For downstreaming OLMv0 components
- **v1**: For downstreaming OLMv1 components

## Key Concepts

### OLMv0 Downstreaming
- Combines three upstream repos into a monorepo structure
- Cherry-picks commits with updated comments
- Sources: [operator-framework/api](https://github.com/`operator-framework/api), [operator-framework/operator-lifecycle-manager](https://github.com/operator-framework/operator-lifecycle-manager), [operator-framework/operator-registry](https://github.com/operator-framework/operator-registry) 
- Target: [openshift/operator-framework-olm](https://github.com/openshift/operator-framework-olm)

### OLMv1 Downstreaming
- Direct mirror of upstream commits (preserving SHAs)
- Handles commit filtering based on prefixes (`UPSTREAM: <drop>:`, `UPSTREAM: <carry>:`)
- Source: [operator-framework/operator-controller](https://github.com/operator-framework/operator-controller)
- Target: [openshift/operator-framework-operator-controller](https://github.com/openshift/operator-framework-operator-controller)

## Common Tasks for AI Agents

### 1. Building the Tools
```bash
make build
# Creates two executables: v0 and v1
```

### 2. Understanding the Codebase Structure
```
.
├── cmd/           # Main entry points for v0 and v1 tools
├── pkg/
│   ├── flags/     # Common flags and options
│   ├── internal/  # Internal utilities
│   ├── v0/        # OLMv0-specific logic
│   └── v1/        # OLMv1-specific logic
├── examples/      # Sample scripts for manual merging
└── *.Dockerfile   # Container definitions for CI/CD
```

### 3. Code Modifications

When modifying code:
- **Read existing patterns**: This codebase uses flag-based configuration extensively
- **Check both v0 and v1**: Changes to common functionality may affect both tools
- **Maintain go.mod cleanliness**: Upstream repos must be `go mod tidy` clean
- **Test locally**: Use the examples directory scripts to test changes

### 4. Adding New Flags/Options

Flags are defined in:
- Generic flags: `pkg/flags/*.go`
- OLMv0 flags: `pkg/v0/*.go`
- OLMv1 flags: `pkg/v1/*.go`

### 5. Understanding Modes

The tools support three modes:
- **Default (no mode)**: List commits to be merged
- **`-mode=synchronize`**: Perform merge in local repository
- **`-mode=publish`**: Perform merge and attempt to publish PR

### 6. Working with Git Operations

The tools perform sophisticated git operations including:
- Cherry-picking with comment updates
- Merge commits with `--strategy=ours`
- Worktree management
- Commit filtering based on prefixes

When debugging or enhancing git operations, pay attention to:
- Commit message formatting
- SHA preservation (especially for v1)
- Cleanup operations to avoid cruft

### 7. CI/CD Integration

The tools run as:
- **Periodic jobs**: Automated downstreaming
  - [OLMv0 periodic](https://prow.ci.openshift.org/?job=periodic-auto-olm-downstreaming)
  - [OLMv1 periodic](https://prow.ci.openshift.org/?job=periodic-auto-olm-v1-downstreaming)
- **Post-submit jobs**: PR updates
  - [OLMv1 post-submit](https://prow.ci.openshift.org/?job=post-ci-openshift-operator-framework-operator-controller-main-refresh-bumper-pr)

Job definitions are in [openshift/release](https://github.com/openshift/release) repo, v0 can be found [here as periodic-auto-olm-downstreaming](https://github.com/openshift/release/blob/668917ee191380409602e4e5b1122eb8966ba909/ci-operator/jobs/infra-periodics.yaml#L1277) and
v1 in [here as periodic-auto-olm-v1-downstreaming](https://github.com/openshift/release/blob/668917ee191380409602e4e5b1122eb8966ba909/ci-operator/jobs/infra-periodics.yaml#L1333)

## Common Agent Workflows

### Analyzing Issues
1. Check if issue is v0 or v1 specific
2. Review relevant code in `pkg/v0/` or `pkg/v1/`
3. Check examples for reproduction scripts
4. Consider impact on periodic jobs

### Implementing Features
1. Determine if feature applies to v0, v1, or both
2. Add necessary flags in appropriate location
3. Implement logic following existing patterns
4. Update examples if needed
5. Consider Dockerfile updates for dependency changes

### Debugging
1. Check logs from periodic/post-submit jobs
2. Use example scripts for local reproduction
3. Verify git operations in a test repository
4. Check for leftover cruft (worktrees, refs, etc.)

### Updating Dependencies
1. Update golang version in both:
   - [CI config](https://github.com/openshift/release/blob/master/ci-operator/config/openshift/operator-framework-tooling/openshift-operator-framework-tooling-main.yaml)
   - Local Dockerfiles
2. Run `go mod tidy`
3. Test builds

## Important Constraints

- **Minimal human interaction**: Tools designed for automation
- **Clean upstream**: Upstream repos must be `go mod tidy` clean
- **Integration upstream**: Repository integration must happen upstream, not during downstreaming
- **Cleanup awareness**: Git operations may leave cruft requiring cleanup

## Testing Locally

Use the examples directory:
```bash
cd examples
# Review and run appropriate example script
```

For cleanup after testing:
```bash
git clean -fdx
git reflog expire --expire-unreachable=now --all
git gc --prune=now
git worktree prune
```

## Links and Resources

- [OLMv0 upstream - operator-lifecycle-manager](https://github.com/operator-framework/operator-lifecycle-manager)
- [OLMv0 downstream](https://github.com/openshift/operator-framework-olm)
- [OLMv1 upstream - operator-controller](https://github.com/operator-framework/operator-controller)
- [OLMv1 downstream](https://github.com/openshift/operator-framework-operator-controller)
- [OpenShift Release Config](https://github.com/openshift/release)

## Agent Tips

1. **Always read before modifying**: Understand existing patterns in the codebase
2. **Consider both tools**: Many changes affect both v0 and v1
3. **Test with examples**: Use example scripts to verify changes work end-to-end
4. **Check CI integration**: Consider impact on periodic and post-submit jobs
5. **Maintain cleanliness**: Follow existing code style and keep dependencies tidy
6. **Git awareness**: These tools perform complex git operations; understand the implications
