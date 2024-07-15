#!/bin/bash
#
# This script will run a local OLMv0 downstream operation, to allow someone to
# debug a failure.
#
# Assumptions:
# * The tooling directory is located in ${HOME}/git/openshift/operator-framework-tooling
# * The v0 executable has been built in there via `make`
#
# Call with your github user name, if it's not the same as your current user name.
#

GITHUB_USER=${1:-${USER}}

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
    if [ -d $1 ]; then
        echo "Resetting $1"
        (cd $1 && reset-repo)
    else
        echo "Creating $1"
        git clone git@github.com:openshift/$1.git
        git -C $1 remote add ${GITHUB_USER} git@github.com:${GITHUB_USER}/$1.git
    fi
}

setup-repo operator-framework-olm

set -x

cd operator-framework-olm
${HOME}/git/openshift/operator-framework-tooling/v0 \
       --mode=synchronize \
       --delay-manifest-generation \
       --log-level=debug
