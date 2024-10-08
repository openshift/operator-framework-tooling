#!/bin/bash
#
# This script will run a local OLMv0 downstream operation, to allow someone to
# debug a failure.
#
# Call with your github user name, if it's not the same as your current user name.
#
# Usage examples:
#     downstream-v1.sh octocat "-drop-commits fca12345678" /Users/octocat/downstreamv0
#     downstream-v1.sh octocat "" /Users/octocat/downstreamv0
#     downstream-v1.sh octocat

GITHUB_USER=${1:-${USER}}
SYNC_ARGS=${2:-""}
SYNC_DIR=${3:-$PWD}

TOOLS_REPO_DIR=$(dirname "$(dirname "$0")")

# Cleanup from last time
reset-repo () {
    git reset HEAD --hard
    git checkout master
    git reset origin/master --hard
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

setup-repo operator-framework-olm

set -x

pushd $TOOLS_REPO_DIR
make
popd

pushd $SYNC_DIR/operator-framework-olm
$TOOLS_REPO_DIR/v0 \
       --mode=synchronize \
       --delay-manifest-generation \
       --log-level=debug \
       $SYNC_ARGS
popd
