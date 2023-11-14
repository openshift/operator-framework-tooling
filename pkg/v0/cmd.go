package v0

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openshift/operator-framework-tooling/pkg/flags"
	"github.com/openshift/operator-framework-tooling/pkg/internal"
	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/cmd/generic-autobumper/bumper"
	"k8s.io/test-infra/prow/config/secret"
	"k8s.io/test-infra/prow/labels"
)

const (
	githubRepo = "operator-framework-olm"
)

func DefaultOptions() Options {
	opts := Options{
		stagingDir: "staging/",
		centralRef: "origin/master",
		history:    1,
		Options:    flags.DefaultOptions(),
	}
	opts.Options.GithubRepo = githubRepo
	return opts
}

type Options struct {
	flags.Options

	stagingDir string
	centralRef string
	history    int
}

func (o *Options) Bind(fs *flag.FlagSet) {
	fs.StringVar(&o.stagingDir, "staging-dir", o.stagingDir, "Directory for staging repositories.")
	fs.StringVar(&o.centralRef, "central-ref", o.centralRef, "Git ref for the central branch that will be updated, used as the base for determining what commits need to be cherry-picked.")
	fs.IntVar(&o.history, "history", o.history, "How many commits back to start searching for missing vendor commits.")

	o.Options.Bind(fs)
}

func (o *Options) Validate() error {
	if err := o.Options.Validate(); err != nil {
		return err
	}

	return nil
}

func Run(ctx context.Context, logger *logrus.Logger, opts Options) error {
	var commits []internal.Commit
	var err error
	if opts.CommitFileInput != "" {
		rawCommits, err := os.ReadFile(opts.CommitFileInput)
		if err != nil {
			return fmt.Errorf("could not read input file: %w", err)
		}
		if err := json.Unmarshal(rawCommits, &commits); err != nil {
			return fmt.Errorf("could not unmarshal input commits: %w", err)
		}
	} else {
		commits, err = detectNewCommits(ctx, logger.WithField("phase", "detect"), opts.stagingDir, opts.centralRef, flags.FetchMode(opts.FetchMode), opts.history)
		if err != nil {
			logger.WithError(err).Fatal("failed to detect commits")
		}
	}

	var missingCommits []internal.Commit
	for _, commit := range commits {
		commitLogger := logger.WithField("commit", commit.Hash)
		missing, err := isCommitMissing(ctx, commitLogger, opts.stagingDir, commit)
		if err != nil {
			commitLogger.WithError(err).Fatal("failed to determine if commit is missing")
		}
		if missing {
			missingCommits = append(missingCommits, commit)
		}
	}

	if opts.CommitFileOutput != "" {
		commitsJson, err := json.Marshal(missingCommits)
		if err != nil {
			return fmt.Errorf("could not marshal commits: %w", err)
		}
		if err := os.WriteFile(opts.CommitFileOutput, commitsJson, 0666); err != nil {
			return fmt.Errorf("could not write commits: %w", err)
		}
	}

	cherryPickAll := func() {
		if err := internal.SetCommitter(ctx, logger.WithField("phase", "setup"), opts.GitName, opts.GitEmail); err != nil {
			logger.WithError(err).Fatal("failed to set committer")
		}
		for i, commit := range missingCommits {
			commitLogger := logger.WithField("commit", commit.Hash)
			commitLogger.Infof("cherry-picking commit %d/%d", i+1, len(missingCommits))
			if err := cherryPick(ctx, commitLogger, commit, opts.GitCommitArgs()); err != nil {
				logger.WithError(err).Fatal("failed to cherry-pick commit")
			}
		}
	}

	if len(missingCommits) == 0 {
		logger.Info("Current repository state is up-to-date with upstream.")
		return nil
	}

	switch flags.Mode(opts.Mode) {
	case flags.Summarize:
		internal.Table(logger, missingCommits)
	case flags.Synchronize:
		cherryPickAll()
	case flags.Publish:
		cherryPickAll()
		gc, err := opts.GitHubOptions.GitHubClient(opts.DryRun)
		if err != nil {
			return fmt.Errorf("error getting GitHub client: %w", err)
		}
		gc.SetMax404Retries(0)

		stdout := bumper.HideSecretsWriter{Delegate: os.Stdout, Censor: secret.Censor}
		stderr := bumper.HideSecretsWriter{Delegate: os.Stderr, Censor: secret.Censor}

		remoteBranch := "synchronize-upstream"
		title := "Synchronize From Upstream Repositories"
		if err := bumper.MinimalGitPush(fmt.Sprintf("https://%s:%s@github.com/%s/%s.git", opts.GithubLogin,
			string(secret.GetTokenGenerator(opts.GitHubOptions.TokenPath)()), opts.GithubLogin, opts.GithubRepo),
			remoteBranch, stdout, stderr, opts.DryRun); err != nil {
			return fmt.Errorf("Failed to push changes.: %w", err)
		}

		var labelsToAdd []string
		if opts.SelfApprove {
			logger.Infof("Self-aproving PR by adding the %q and %q labels", labels.Approved, labels.LGTM)
			labelsToAdd = append(labelsToAdd, labels.Approved, labels.LGTM)
		}
		if err := bumper.UpdatePullRequestWithLabels(gc, opts.GithubOrg, opts.GithubRepo, title,
			internal.GetBody(commits, strings.Split(opts.Assign, ",")), opts.GithubLogin+":"+remoteBranch, opts.PRBaseBranch, remoteBranch, true, labelsToAdd, opts.DryRun); err != nil {
			return fmt.Errorf("PR creation failed.: %w", err)
		}
	}
	return nil
}

var commitRegex = regexp.MustCompile(`Upstream-commit: ([a-f0-9]+)\n`)

func detectNewCommits(ctx context.Context, logger *logrus.Entry, stagingDir, centralRef string, mode flags.FetchMode, history int) ([]internal.Commit, error) {
	lastCommits := map[string]string{}
	if err := fs.WalkDir(os.DirFS(stagingDir), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d == nil || !d.IsDir() {
			return nil
		}

		if path == "." {
			return nil
		}
		logger = logger.WithField("repo", path)
		logger.Debug("detecting commits")
		output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "log",
			centralRef,
			"-n", strconv.Itoa(history),
			"--grep", "Upstream-repository: "+path,
			"--grep", "Upstream-commit",
			"--all-match",
			"--pretty=%B",
			"--reverse",
			"--",
			filepath.Join(stagingDir, path),
		))
		if err != nil {
			return err
		}
		var lastCommit string
		commitMatches := commitRegex.FindStringSubmatch(output)
		if len(commitMatches) > 0 {
			if len(commitMatches[0]) > 1 {
				lastCommit = string(commitMatches[1])
			}
		}
		if lastCommit != "" {
			logger.WithField("commit", lastCommit).Debug("found last commit synchronized with staging")
			lastCommits[path] = lastCommit
		}

		if path != "." {
			return fs.SkipDir
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to walk %s: %w", stagingDir, err)
	}

	commits := map[string][]internal.Commit{}
	for repo, lastCommit := range lastCommits {
		var remote string
		switch mode {
		case flags.SSH:
			remote = "git@github.com:operator-framework/" + repo
		case flags.HTTPS:
			remote = "https://github.com/operator-framework/" + repo + ".git"
		}
		if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "fetch",
			remote,
			"master",
		)); err != nil {
			return nil, err
		}

		output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "log",
			"--pretty=%H",
			"--no-merges",
			lastCommit+"...FETCH_HEAD",
		))
		if err != nil {
			return nil, err
		}

		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				commit, err := internal.Info(ctx, logger, line, ".")
				if err != nil {
					return nil, err
				}
				commit.Repo = repo
				if _, ok := commits[repo]; !ok {
					commits[repo] = []internal.Commit{}
				}
				commits[repo] = append(commits[repo], commit)
			}
		}
	}
	// we would like to intertwine the commits from each upstream repository by date, while
	// keeping the order of commits from any one repository in the order they were committed in
	var orderedCommits []internal.Commit
	indices := map[string]int{}
	for repo := range commits {
		indices[repo] = 0
	}
	for {
		// find which repo's commit stack we should pop off to get the next earliest commit
		nextTime := time.Now()
		var nextRepo string
		for repo, index := range indices {
			if commits[repo][index].Date.Before(nextTime) {
				nextTime = commits[repo][index].Date
				nextRepo = repo
			}
		}

		// pop the commit, add it to our list and do housekeeping for our index records
		orderedCommits = append(orderedCommits, commits[nextRepo][indices[nextRepo]])
		if indices[nextRepo] == len(commits[nextRepo])-1 {
			delete(indices, nextRepo)
		} else {
			indices[nextRepo] += 1
		}

		if len(indices) == 0 {
			break
		}
	}

	// our ordered list is descending, but we need to cherry-pick from the oldest first
	var reversedCommits []internal.Commit
	for i := range orderedCommits {
		reversedCommits = append(reversedCommits, orderedCommits[len(orderedCommits)-i-1])
	}
	return reversedCommits, nil
}

func isCommitMissing(ctx context.Context, logger *logrus.Entry, stagingDir string, c internal.Commit) (bool, error) {
	output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
		"git", "log",
		"-n", "1",
		"--grep", "Upstream-repository: "+c.Repo,
		"--grep", "Upstream-commit: "+c.Hash,
		"--all-match",
		"--pretty=%B",
		"--",
		filepath.Join(stagingDir, c.Repo),
	))
	if err != nil {
		return false, err
	}
	return len(output) == 0, nil
}

func cherryPick(ctx context.Context, logger *logrus.Entry, c internal.Commit, commitArgs []string) error {
	{
		output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "cherry-pick",
			"--allow-empty", "--keep-redundant-commits",
			"-Xsubtree=staging/"+c.Repo, c.Hash,
		))
		if err != nil {
			if strings.Contains(output, "vendor/modules.txt deleted in HEAD and modified in") {
				// we remove vendor directories for everything under staging/, but some of the upstream repos have them
				if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
					"git", "rm", "--cached", "-r", "--ignore-unmatch", "staging/"+c.Repo+"/vendor",
				)); err != nil {
					return err
				}
				if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
					"git", "cherry-pick", "--continue",
				)); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	for _, cmd := range []*exec.Cmd{
		internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "tidy",
		), os.Environ()...),
		internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "vendor",
		), os.Environ()...),
		internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "verify",
		), os.Environ()...),
		internal.WithDir(internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "tidy",
		), os.Environ()...), filepath.Join("staging", c.Repo)),
		internal.WithDir(internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "vendor",
		), os.Environ()...), filepath.Join("staging", c.Repo)),
		internal.WithDir(internal.WithEnv(exec.CommandContext(ctx,
			"go", "mod", "verify",
		), os.Environ()...), filepath.Join("staging", c.Repo)),
		internal.WithEnv(exec.CommandContext(ctx,
			"make", "generate-manifests",
		), os.Environ()...),
		exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"--amend", "--allow-empty", "--no-edit",
				"--trailer", "Upstream-repository: " + c.Repo,
				"--trailer", "Upstream-commit: " + c.Hash,
				"staging/" + c.Repo,
				"vendor", "go.mod", "go.sum",
				"manifests", "pkg/manifests"},
				commitArgs...)...,
		),
	} {
		if _, err := internal.RunCommand(logger, cmd); err != nil {
			return err
		}
	}

	return nil
}
