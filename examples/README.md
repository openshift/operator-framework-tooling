# Downstreaming Examples

These scripts will run the downstreaming tooling to allow you to debug a failing
downstreaming job.

## How to Run

You should run this in an new temporary directory, or a directory dedicated to downstreaming.
It _will_ mess up existing repositories, so the scripts create new/clean repositories as needed.

If your GitHub username is different than your local account username, add it to the command line.

OLMv1:
```
$ mkdir ${HOME}/downstream1
$ cd ${HOME}/downstream1
$ ${HOME}/path/to/operator-framework-tooling/examples/downstream-v1.sh -u ${GITHUB_USER}
```

OLMv0 would be similar.

## Cleanup

To really clean up and start fresh, delete the repositories. The scripts try their best to clean
and reuse existing repos, but aren't always successful.

```
$ rm -rf ${HOME}/downstream1/operator-framework-*
```
