#!/bin/bash
#
# This script will run a local OLMv1 downstream operation, to allow someone to
# debug a failure.
#
# Assumptions:
# * The tooling directory is located in ${HOME}/git/openshift/operator-framework-tooling
# * The v1 executable has been built in there via `make`
#
# Call with your github user name, if it's not the same as your current user name.
#

GITHUB_USER=${1:-${USER}}

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
    if [ -d $1 ]; then
        echo "Resetting $1"
        (cd $1 && reset-repo)
    else
        echo "Creating $1"
        git clone git@github.com:openshift/$1.git
        git -C $1 remote add ${GITHUB_USER} git@github.com:${GITHUB_USER}/$1.git
    fi
}

setup-repo operator-framework-catalogd
setup-repo operator-framework-operator-controller

${HOME}/git/openshift/operator-framework-tooling/v1 \
       --mode=synchronize \
       --catalogd-dir=${PWD}/operator-framework-catalogd \
       --operator-controller-dir=${PWD}/operator-framework-operator-controller \
       --pause-on-cherry-pick-error \
       --log-level=debug

# Additional option: https://github.com/openshift/operator-framework-tooling/pull/44
#       --delay-manifest-generation \
