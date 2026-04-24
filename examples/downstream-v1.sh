#!/bin/bash
set -euo pipefail

usage() {
    echo "This script will run a local OLMv1 downstream operation, to allow someone to"
    echo "debug a failure. It will push the resulting branch and create a pull request."
    echo ""
    echo "Requires the 'gh' CLI to be authenticated ('gh auth login') before running."
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
GITHUB_TOKEN_PATH="$(mktemp "${HOME}/.github-token.XXXXXX")"
trap 'rm -f "$GITHUB_TOKEN_PATH"' EXIT

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
    git checkout main
    git reset origin/main --hard
    git branch -D synchronize || true # It's ok if it doesnt' exist
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

setup-repo operator-framework-operator-controller

make -C $TOOLS_REPO_DIR

if ! command -v gh &> /dev/null; then
    echo "error: 'gh' CLI not found in PATH. Install it and run 'gh auth login' first."
    exit 1
fi

# Create the GitHub token file from the gh CLI's stored credentials.
# Run 'gh auth login' first if you haven't already.
gh auth token > "${GITHUB_TOKEN_PATH}"
chmod 600 "${GITHUB_TOKEN_PATH}"

set -x

${TOOLS_REPO_DIR}/v1  \
       --mode=publish \
       --dry-run=false \
       --github-login=${GITHUB_USER} \
       --github-token-path=${GITHUB_TOKEN_PATH} \
       --operator-controller-dir=${SYNC_DIR}/operator-framework-operator-controller \
       --pause-on-cherry-pick-error \
       --log-level=debug \
       ${SYNC_ARGS}

# Additional option: https://github.com/openshift/operator-framework-tooling/pull/44
#       --delay-manifest-generation \
