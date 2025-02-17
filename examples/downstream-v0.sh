#!/bin/bash
set -euo pipefail

usage() {
    echo "This script will run a local OLMv0 downstream operation, to allow someone to"
    echo "debug a failure."
    echo ""
    echo "Usage: $0 [-u github-username] [-a \"-extra-sync args\"] [-d /path/to/the/sync/dir]"
    echo ""
    echo "Options:"
    echo " -u GitHub username. Default: current system user."
    echo " -a Additional arguments to the syncer."
    echo " -d Path to the sync dir. Default: current working directory."
    echo ""
}

GITHUB_USER=$USER
SYNC_ARGS=""
SYNC_DIR=$PWD
SYNC_BRANCH=master

# Get the options
while getopts "hu:a:d:" option; do
   case $option in
      u)
        GITHUB_USER=$OPTARG;;
      a)
        SYNC_ARGS=$OPTARG;;
      d)
        SYNC_DIR=$OPTARG;;
      h)
        usage
        exit 1;;
     \?)
        usage
        exit 1;;
   esac
done

# Get an absolute path of the root of the tooling repo
# based on the path of this script.
TOOLS_REPO_DIR=$(dirname "$( cd $(dirname $0) ; pwd)")

# Cleanup from last time
reset-repo () {
    git reset HEAD --hard
    # if the main branch exists, use that instead
    if git show-ref --verify --quiet refs/heads/main; then
        SYNC_BRANCH=main
    fi
    git checkout ${SYNC_BRANCH}
    git reset origin/${SYNC_BRANCH} --hard
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

make -C $TOOLS_REPO_DIR

set -x

pushd $SYNC_DIR/operator-framework-olm
${TOOLS_REPO_DIR}/v0 \
       --mode=synchronize \
       --delay-manifest-generation \
       --log-level=debug \
       ${SYNC_ARGS}
popd
