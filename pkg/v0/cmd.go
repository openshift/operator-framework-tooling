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

	semver "github.com/Masterminds/semver/v3"
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

var depRepos = []string{
	"operator-framework/api",
	"operator-framework/operator-registry",
}

func DefaultOptions() Options {
	opts := Options{
		stagingDir: "staging/",
		centralRef: "origin/master",
		history:    1,
		Options:    flags.DefaultOptions(),
	}
	opts.Options.GithubRepo = githubRepo
	opts.Options.DelayManifestGeneration = true
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

func resolveCentralRef(ctx context.Context, logger *logrus.Entry, origCentralRef string) (string, error) {
	output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
		"git", "log",
		"-n", "1",
		"--pretty=%H",
		origCentralRef,
	))
	if err != nil {
		return "", err
	}
	newCentralRef := strings.TrimSpace(output)
	if newCentralRef == "" {
		return "", fmt.Errorf("resolved central-ref is empty")
	}
	logger.WithField("commit", newCentralRef).WithField("central-ref", origCentralRef).Debug("resolved central-ref")
	return newCentralRef, nil
}

func Run(ctx context.Context, logger *logrus.Logger, opts Options) error {
	var commits []internal.Commit
	if opts.CommitFileInput != "" {
		rawCommits, err := os.ReadFile(opts.CommitFileInput)
		if err != nil {
			return fmt.Errorf("could not read input file: %w", err)
		}
		if err := json.Unmarshal(rawCommits, &commits); err != nil {
			return fmt.Errorf("could not unmarshal input commits: %w", err)
		}
	} else {
		// if opts.centralRef is modified (i.e. FETCH_HEAD), calculateRepoRefs is going to mess up that calculation,
		// so resolve opts.centralRef first
		centralRef, err := resolveCentralRef(ctx, logger.WithField("phase", "resolve central-ref"), opts.centralRef)
		if err != nil {
			logger.WithError(err).Fatal("failed to resolve central-ref")
		}
		repoRefs, err := calculateRepoRefs(ctx, logger.WithField("phase", "calculate refs"), opts)
		if err != nil {
			logger.WithError(err).Fatal("failed to determine repository references")
		}
		commits, err = detectNewCommits(ctx, logger.WithField("phase", "detect"), opts.stagingDir, centralRef, repoRefs, flags.FetchMode(opts.FetchMode), opts.history)
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
			commitLogger := logger.WithField("commit", commit.Hash).WithField("repo", commit.Repo)
			commitLogger.Infof("cherry-picking commit %d/%d", i+1, len(missingCommits))
			delay := opts.DelayManifestGeneration
			if i+1 == len(missingCommits) {
				// we are on the last commit, we need to run the delayed commands
				delay = false
			}
			if err := cherryPick(ctx, commitLogger, commit, opts.GitCommitArgs(), delay); err != nil {
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
		internal.Table(logger, missingCommits, "operator-framework/")
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
		title := "NO-ISSUE: Synchronize From Upstream Repositories"
		if err := bumper.MinimalGitPush(fmt.Sprintf("https://%s:%s@github.com/%s/%s.git", opts.GithubLogin,
			string(secret.GetTokenGenerator(opts.GitHubOptions.TokenPath)()), opts.GithubLogin, opts.GithubRepo),
			remoteBranch, stdout, stderr, opts.DryRun); err != nil {
			return fmt.Errorf("Failed to push changes.: %w", err)
		}

		var labelsToAdd []string
		if opts.SelfApprove {
			logger.Infof("Self-approving PR by adding the %q and %q labels", labels.Approved, labels.LGTM)
			labelsToAdd = append(labelsToAdd, labels.Approved, labels.LGTM)
		}
		if err := bumper.UpdatePullRequestWithLabels(gc, opts.GithubOrg, opts.GithubRepo, title,
			internal.GetBody(commits, strings.Split(opts.Assign, ",")), opts.GithubLogin+":"+remoteBranch, opts.PRBaseBranch, remoteBranch, true, labelsToAdd, opts.DryRun); err != nil {
			return fmt.Errorf("PR creation failed.: %w", err)
		}
	}
	return nil
}

func getTagOrCommit(ctx context.Context, repo string, dir string, opts Options, logger *logrus.Entry) (string, error) {

	// Create temporary

	module := fmt.Sprintf("github.com/%s", repo)
	rawInfo, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"go", "list", "-json", "-m", module), dir))
	if err != nil {
		return "", fmt.Errorf("failed to determine dependent version for module %s: %w", module, err)
	}
	var info struct {
		Version string `json:"Version"`
	}
	if err := json.Unmarshal([]byte(rawInfo), &info); err != nil {
		return "", fmt.Errorf("failed to parse module version for %s: %w", module, err)
	}
	logger.WithFields(logrus.Fields{"repo": repo, "version": info.Version}).Info("resolved latest version")

	v, err := semver.NewVersion(info.Version)
	if err != nil {
		return "", err
	}
	// If this does not have a Prerelease, then we just return the version string
	pre := v.Prerelease()
	if pre == "" {
		return info.Version, nil
	}
	// It's a pre-release version, which we assume is in DATE-COMMIT format
	pres := strings.Split(pre, "-")
	if len(pres) != 2 {
		return "", fmt.Errorf("Bad prerelease: %q", info.Version)
	}
	// Return the second component, which is a commit SHA
	return pres[1], nil
}

func calculateRepoRefs(ctx context.Context, logger *logrus.Entry, opts Options) (map[string]string, error) {
	repoRefs := map[string]string{}

	// for operator-lifecycle-manager, always use master
	repoRefs["operator-framework/operator-lifecycle-manager"] = "master"

	// Create a temporary worktree of upstream OLM to figure out what dependency versions we are moving to
	var remote string

	switch flags.FetchMode(opts.FetchMode) {
	case flags.SSH:
		remote = "git@github.com:operator-framework/operator-lifecycle-manager"
	case flags.HTTPS:
		remote = "https://github.com/operator-framework/operator-lifecycle-manager.git"
	}
	if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
		"git", "fetch",
		remote,
		"master",
	)); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "olm")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
		"git", "worktree",
		"add",
		dir,
		"FETCH_HEAD",
	)); err != nil {
		return nil, err
	}

	for _, repo := range depRepos {
		tag, err := getTagOrCommit(ctx, repo, dir, opts, logger.WithField("phase", "version scan"))
		if err != nil {
			logger.Fatalf("Error processing version for %q: %v", repo, err)
			continue
		}

		var remote string
		switch flags.FetchMode(opts.FetchMode) {
		case flags.SSH:
			remote = "git@github.com:" + repo
		case flags.HTTPS:
			remote = "https://github.com/" + repo + ".git"
		}
		if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "fetch",
			remote,
			tag,
		)); err != nil {
			return nil, err
		}
		output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "log",
			"-n", "1",
			"--pretty=%H",
			"--no-merges",
			"FETCH_HEAD",
		))
		if err != nil {
			return nil, err
		}
		repoRefs[repo] = strings.TrimSpace(output)
		if repoRefs[repo] == "" {
			return nil, fmt.Errorf("unable to find commit at %q for %q", tag, repo)
		}

		repoLogger := logger.WithField("repo", repo).WithField("commit", repoRefs[repo])
		if tag == repoRefs[repo] {
			repoLogger.Info("found commit")
		} else {
			repoLogger.WithField("tag", tag).Info("mapped tag to commit")
		}
	}
	return repoRefs, nil
}

var commitRegex = regexp.MustCompile(`Upstream-commit: ([a-f0-9]+)\n`)

func detectNewCommits(ctx context.Context, logger *logrus.Entry, stagingDir, centralRef string, repoRefs map[string]string, mode flags.FetchMode, history int) ([]internal.Commit, error) {
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
		walkLogger := logger.WithField("repo", path)
		walkLogger.Debug("detecting commits")
		output, err := internal.RunCommand(walkLogger, exec.CommandContext(ctx,
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
			walkLogger.WithField("commit", lastCommit).Debug("found last commit synchronized with staging")
			lastCommits[path] = lastCommit
		} else {
			walkLogger.Fatal("did not find the last commit synchronized with staging")
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
		repoLogger := logger.WithField("repo", repo)
		var remote string
		switch mode {
		case flags.SSH:
			remote = "git@github.com:operator-framework/" + repo
		case flags.HTTPS:
			remote = "https://github.com/operator-framework/" + repo + ".git"
		}

		ref, ok := repoRefs["operator-framework/"+repo]
		if !ok {
			return nil, fmt.Errorf("ref not found for %q", repo)
		}
		repoLogger.WithField("ref", ref).Debug("found fetch reference")
		if _, err := internal.RunCommand(repoLogger, exec.CommandContext(ctx,
			"git", "fetch",
			remote,
			ref,
		)); err != nil {
			return nil, err
		}

		output, err := internal.RunCommand(repoLogger, exec.CommandContext(ctx,
			"git", "log",
			"--pretty=%H",
			"--no-merges",
			lastCommit+"...FETCH_HEAD",
		))
		if err != nil {
			// This could be due to the lastCommit being beyond the tag, in this case,
			// we'd see an "Invalid symmetric difference expression" error.
			// If so, fetch the master branch, and then see if the latestCommit is in there.
			// If it is, then downstream has moved beyond "where it should be".
			// This is ok, we shouldn't error out
			if !strings.Contains(output, "Invalid symmetric difference expression") {
				return nil, err
			}
			repoLogger.Debug("checking if downtream has moved beyond expected commit")
			if _, err2 := internal.RunCommand(repoLogger, exec.CommandContext(ctx,
				"git", "fetch",
				remote,
				"master",
			)); err2 != nil {
				return nil, err2
			}
			if _, err2 := internal.RunCommand(repoLogger, exec.CommandContext(ctx,
				"git", "log",
				"--pretty=%H",
				"--no-merges",
				lastCommit+"...FETCH_HEAD",
			)); err2 != nil {
				// Still getting an error, so return the original `err`
				return nil, err
			}
			// Otherwise, downstream is ahead of where it should be, so issue a warning
			repoLogger.WithField("last-commit", lastCommit).WithField("expected", ref).Warn("downstream has moved beyond expected commit")
			// And reset the output to blank
			output = ""
		}

		for _, line := range strings.Split(output, "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				commit, err := internal.Info(ctx, repoLogger, line, ".")
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
		if len(commits[repo]) > 0 {
			repoLogger.WithField("commits", len(commits[repo])).Debug("found commits")
		} else {
			repoLogger.Debug("no commits found")
		}
	}
	// No commits? No work.
	if len(commits) == 0 {
		logger.Debug("no commits found to merge over all repos")
		return nil, nil
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

func cherryPick(ctx context.Context, logger *logrus.Entry, c internal.Commit, commitArgs []string, delayManifestGeneration bool) error {
	{
		output, err := internal.RunCommand(logger, exec.CommandContext(ctx,
			"git", "cherry-pick",
			"--allow-empty", "--keep-redundant-commits",
			"-Xsubtree=staging/"+c.Repo, c.Hash,
		))
		if err != nil {
			continueCherryPick := false
			if strings.Contains(output, "vendor/modules.txt deleted in HEAD and modified in") {
				continueCherryPick = true
				// we remove vendor directories for everything under staging/, but some of the upstream repos have them
				if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
					"git", "rm", "--cached", "-r", "--ignore-unmatch", "staging/"+c.Repo+"/vendor",
				)); err != nil {
					return err
				}
			}
			if strings.Contains(output, "Merge conflict in staging/"+c.Repo+"/go.mod") {
				continueCherryPick = true
				// Due to the `go mod` commands in the staging directory below, this file may have conflicts,
				// So resolve it as "theirs" (i.e. incoming), and then use the `go mod` commands to update it
				// Conflicts can arise due to downstream-only code in a staging directory affecting the
				// `go mod` command results
				if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
					"git", "checkout", "--theirs", "--", "staging/"+c.Repo+"/go.mod",
				)); err != nil {
					return err
				}
				if _, err := internal.RunCommand(logger, exec.CommandContext(ctx,
					"git", "add", "staging/"+c.Repo+"/go.mod",
				)); err != nil {
					return err
				}
			}
			if continueCherryPick {
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

	gomod := []*exec.Cmd{
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
	}

	manifests := []*exec.Cmd{
		internal.WithEnv(exec.CommandContext(ctx,
			"make", "generate-manifests",
		), os.Environ()...),
	}

	commits := []*exec.Cmd{
		// Necessary for untracked files created via `go mod vendor`
		exec.CommandContext(ctx,
			"git", "add", "vendor",
		),
		exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"--amend", "--allow-empty", "--no-edit",
				"--trailer", "Upstream-repository: " + c.Repo,
				"--trailer", "Upstream-commit: " + c.Hash,
				"staging/" + c.Repo,
				"vendor", "go.mod", "go.sum",
				"manifests", "microshift-manifests", "pkg/manifests"},
				commitArgs...)...,
		),
	}

	commands := gomod
	if !delayManifestGeneration {
		commands = append(commands, manifests...)
	}
	commands = append(commands, commits...)

	for _, cmd := range commands {
		if _, err := internal.RunCommand(logger, cmd); err != nil {
			return err
		}
	}

	return nil
}
