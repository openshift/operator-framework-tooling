#!/bin/bash
#
# This script will run a local OLMv1 downstream operation, to allow someone to
# debug a failure.
#
# Call with your github user name, if it's not the same as your current user name.
#
# Usage examples:
#     downstream-v1.sh octocat "-drop-commits fca12345678" /Users/octocat/downstream1
#     downstream-v1.sh octocat "" /Users/octocat/downstream1
#     downstream-v1.sh octocat

GITHUB_USER=${1:-${USER}}
SYNC_ARGS=${2:-""}
SYNC_DIR=${3:-$PWD}

TOOLS_REPO_DIR=$(dirname "$(dirname "$0")")

# Cleanup from last time
reset-repo () {
    git reset HEAD --hard
    git checkout main
    git reset origin/main --hard
    git branch -D synchronize
    git clean -fdx
    git reflog expire --expire-unreachable=now --all
    git gc --prune=now
    git worktree prune
    git fetch origin
    git pull
}

setup-repo() {
    pushd $SYNC_DIR
    if [ -d $1 ]; then
        echo "Resetting $1"
        (cd $1 && reset-repo)
    else
        echo "Creating $1"
        git clone git@github.com:openshift/$1.git
        git -C $1 remote add ${GITHUB_USER} git@github.com:${GITHUB_USER}/$1.git
    fi
    popd
}

setup-repo operator-framework-catalogd
setup-repo operator-framework-operator-controller

pushd $TOOLS_REPO_DIR
make
popd

$TOOLS_REPO_DIR/v1  \
       --mode=synchronize \
       --catalogd-dir=${SYNC_DIR}/operator-framework-catalogd \
       --operator-controller-dir=${SYNC_DIR}/operator-framework-operator-controller \
       --pause-on-cherry-pick-error \
       --log-level=debug \
       $SYNC_ARGS

# Additional option: https://github.com/openshift/operator-framework-tooling/pull/44
#       --delay-manifest-generation \
