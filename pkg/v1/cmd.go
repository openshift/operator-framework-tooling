package v1

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/openshift/operator-framework-tooling/pkg/flags"
	"github.com/openshift/operator-framework-tooling/pkg/internal"
	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/cmd/generic-autobumper/bumper"
	"k8s.io/test-infra/prow/config/secret"
	"k8s.io/test-infra/prow/labels"
	"sigs.k8s.io/yaml"
)

const (
	defaultBranch = "main"
)

func DefaultOptions() Options {
	opts := Options{
		Options: flags.DefaultOptions(),
	}
	opts.Options.PRBaseBranch = defaultBranch
	return opts
}

type Options struct {
	rukpakDir             string
	operatorControllerDir string
	catalogDDir           string

	flags.Options
}

func (o *Options) Bind(fs *flag.FlagSet) {
	fs.StringVar(&o.rukpakDir, "rukpak-dir", o.rukpakDir, "Directory for rukpak repository.")
	fs.StringVar(&o.operatorControllerDir, "operator-controller-dir", o.operatorControllerDir, "Directory for operator-controller repository.")
	fs.StringVar(&o.catalogDDir, "catalogd-dir", o.catalogDDir, "Directory for catalogd repository.")

	o.Options.Bind(fs)
}

func (o *Options) Validate() error {
	if err := o.Options.Validate(); err != nil {
		return err
	}

	for name, val := range map[string]string{
		"rukpak":              o.rukpakDir,
		"operator-controller": o.operatorControllerDir,
		"catalogd":            o.catalogDDir,
	} {
		if val == "" {
			return fmt.Errorf("--%s-dir is required", name)
		}
	}
	return nil
}

// Config describes how to update a repo to the intended state.
type Config struct {
	Target     internal.Commit   `json:"target"`
	Additional []internal.Commit `json:"additional"`
}

func Run(ctx context.Context, logger *logrus.Logger, opts Options) error {
	directories := map[string]string{
		"operator-controller": opts.operatorControllerDir,
		"rukpak":              opts.rukpakDir,
		"catalogd":            opts.catalogDDir,
	}
	commits := map[string]Config{}
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
		commits, err = detectNewCommits(ctx, logger.WithField("phase", "detect"), directories, flags.FetchMode(opts.FetchMode))
		if err != nil {
			logger.WithError(err).Fatal("failed to detect commits")
		}
	}

	if opts.CommitFileOutput != "" {
		commitsJson, err := json.Marshal(commits)
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
		for repo, config := range commits {
			commitLogger := logger.WithField("repo", repo)
			if err := applyConfig(ctx, commitLogger, "operator-framework", repo, "main", directories[repo], config, opts.GitCommitArgs()); err != nil {
				logger.WithError(err).Fatal("failed to merge to upstream")
			}
		}
		// we need the operator-framework-operator-controller go.mod to point to the downstream libraries
		// that we're synchronizing above, but we can't have replace directives in the go.mod until the
		// downstream repositories have the desired git state already published. Therefore, only if we
		// found that the repos are up-to-date (they are not in the commits map) can we do the replacing.
		otherCommits := map[string]string{}
		for _, repo := range []string{"rukpak", "catalogd"} {
			if _, ok := commits[repo]; !ok {
				commit, err := determineDownstreamHead(ctx, logger.WithField("repo", repo), directories[repo], repo, flags.FetchMode(opts.FetchMode))
				if err != nil {
					logger.WithError(err).Fatal("failed to determine other repo HEAD")
				}
				otherCommits[repo] = commit
			}
		}
		delete(otherCommits, "operator-controller")
		if err := rewriteGoMod(ctx, logger.WithField("repo", "operator-controller"), directories["operator-controller"], otherCommits, opts.GitCommitArgs()); err != nil {
			logger.WithError(err).Fatal("failed to rewrite go mod")
		}
	}

	switch flags.Mode(opts.Mode) {
	case flags.Summarize:
		for repo, info := range commits {
			fmt.Printf("openshift/operator-framework-%s: updating to:\n", repo)
			internal.Table(logger, []internal.Commit{info.Target})
			fmt.Println(" + additional commits to cherry-pick on top:")
			internal.Table(logger, info.Additional)
			fmt.Println()
		}
	case flags.Synchronize:
		cherryPickAll()
	case flags.Publish:
		client, err := opts.GitHubClient(opts.DryRun)
		if err != nil {
			return fmt.Errorf("failed to create a GitHub client: %w", err)
		}

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
		for repo, config := range commits {
			fork, err := client.EnsureFork(opts.GithubLogin, "openshift", "operator-framework-"+repo)
			if err != nil {
				return fmt.Errorf("could not ensure fork: %w", err)
			}

			if err := bumper.MinimalGitPush(
				fmt.Sprintf(
					"https://%s:%s@github.com/%s/%s.git",
					opts.GithubLogin, string(secret.GetTokenGenerator(opts.GitHubOptions.TokenPath)()), opts.GithubLogin, fork,
				),
				remoteBranch, stdout, stderr, opts.DryRun, bumper.WithContext(ctx), bumper.WithDir(directories[repo])); err != nil {
				return fmt.Errorf("Failed to push changes.: %w", err)
			}

			var labelsToAdd []string
			if opts.SelfApprove {
				logger.Infof("Self-aproving PR by adding the %q and %q labels", labels.Approved, labels.LGTM)
				labelsToAdd = append(labelsToAdd, labels.Approved, labels.LGTM)
			}
			if err := bumper.UpdatePullRequestWithLabels(gc, opts.GithubOrg, fork, title,
				internal.GetBody(config.Additional, strings.Split(opts.Assign, ",")), opts.GithubLogin+":"+remoteBranch, opts.PRBaseBranch, remoteBranch, true, labelsToAdd, opts.DryRun); err != nil {
				return fmt.Errorf("PR creation failed.: %w", err)
			}
		}
	}
	return nil
}

func determineDownstreamHead(ctx context.Context, logger *logrus.Entry, dir, repo string, mode flags.FetchMode) (string, error) {
	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "fetch", "--tags", downstreamRemote(repo, mode),
	), dir)); err != nil {
		return "", fmt.Errorf("failed to fetch upstream: %w", err)
	}
	commitSha, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "rev-parse", "FETCH_HEAD",
	), dir))
	if err != nil {
		return "", fmt.Errorf("failed to parse upstream HEAD: %w", err)
	}
	return strings.TrimSpace(commitSha), nil
}

var syntheticVersionRegex = regexp.MustCompile(`[^-]+-[0-9]+-([[0-9a-f]+)`)

func upstreamRemote(repo string, mode flags.FetchMode) string {
	switch mode {
	case flags.SSH:
		return "git@github.com:operator-framework/" + repo + ".git"
	case flags.HTTPS:
		return "https://github.com/operator-framework/" + repo + ".git"
	default:
		panic(fmt.Errorf("unexpected fetch mode %s", mode))
	}
}

func downstreamRemote(repo string, mode flags.FetchMode) string {
	switch mode {
	case flags.SSH:
		return "git@github.com:openshift/operator-framework-" + repo + ".git"
	case flags.HTTPS:
		return "https://github.com/openshift/operator-framework-" + repo + ".git"
	default:
		panic(fmt.Errorf("unexpected fetch mode %s", mode))
	}
}

func detectNewCommits(ctx context.Context, logger *logrus.Entry, directories map[string]string, mode flags.FetchMode) (map[string]Config, error) {
	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "fetch", "--tags", upstreamRemote("operator-controller", mode),
	), directories["operator-controller"])); err != nil {
		return nil, fmt.Errorf("failed to fetch upstream: %w", err)
	}

	target := map[string]Config{}
	config, err := detectNewOperatorControllerCommits(ctx, logger, directories["operator-controller"], mode)
	if err != nil {
		return nil, err
	}
	if config != nil {
		target["operator-controller"] = *config
	}

	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "checkout", target["operator-controller"].Target.Hash,
	), directories["operator-controller"])); err != nil {
		return nil, fmt.Errorf("failed to check out upstream target: %w", err)
	}

	for _, name := range []string{"rukpak", "catalogd"} {
		module := fmt.Sprintf("github.com/operator-framework/%s", name)
		rawInfo, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"go", "list", "-json", "-m", module,
		), directories["operator-controller"]))
		if err != nil {
			return nil, fmt.Errorf("failed to determine dependent version in modules: %w", err)
		}
		var info struct {
			Version string `json:"Version"`
		}
		if err := json.Unmarshal([]byte(rawInfo), &info); err != nil {
			return nil, fmt.Errorf("failed to parse module version info for %s: %w", module, err)
		}
		logger.WithFields(logrus.Fields{"repo": name, "version": info.Version}).Info("resolved latest version")

		if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "fetch", "--tags", upstreamRemote(name, mode),
		), directories[name])); err != nil {
			return nil, fmt.Errorf("failed to fetch upstream version: %w", err)
		}

		commitSha, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "rev-parse", info.Version+"^{}", // get the commit the tag points to, if the tag is its own object
		), directories[name]))
		if err != nil {
			// it's possible that the version is synthetic v0.0.0-date-sha, so check for that
			var commitFromVersion string
			commitMatches := syntheticVersionRegex.FindStringSubmatch(commitSha)
			if len(commitMatches) > 0 {
				if len(commitMatches[0]) > 1 {
					commitFromVersion = commitMatches[1]
				}
			}
			if commitFromVersion == "" {
				return nil, fmt.Errorf("could not determine commit for %s from go.mod version: %w", name, err)
			}
			commitSha = commitFromVersion
		}
		commitSha = strings.TrimSpace(commitSha)
		logger.WithFields(logrus.Fields{"repo": name, "commit": commitSha}).Info("resolved latest commit")
		commit, err := internal.Info(ctx, logger, commitSha, directories[name])
		if err != nil {
			return nil, fmt.Errorf("failed to determine commit info: %w", err)
		}
		if isUpToDate(ctx, logger, name, directories[name], commit.Hash) {
			continue
		}
		additional, err := detectCarryCommits(ctx, logger, name, directories[name], commit.Hash, mode)
		if err != nil {
			return nil, err
		}
		target[name] = Config{
			Target:     commit,
			Additional: additional,
		}
	}
	return target, nil
}

func detectNewOperatorControllerCommits(ctx context.Context, logger *logrus.Entry, dir string, mode flags.FetchMode) (*Config, error) {
	commitSha, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "rev-parse", "FETCH_HEAD",
	), dir))
	if err != nil {
		return nil, fmt.Errorf("failed to parse upstream HEAD: %w", err)
	}
	commitSha = strings.TrimSpace(commitSha)
	logger.WithFields(logrus.Fields{"repo": "operator-controller", "commit": commitSha}).Info("resolved latest commit")
	commit, err := internal.Info(ctx, logger, commitSha, dir)
	if err != nil {
		return nil, fmt.Errorf("failed to determine commit info: %w", err)
	}
	if isUpToDate(ctx, logger, "operator-controller", dir, commit.Hash) {
		return nil, nil
	}
	additional, err := detectCarryCommits(ctx, logger, "operator-controller", dir, commit.Hash, mode)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve additional commits: %w", err)
	}
	return &Config{
		Target:     commit,
		Additional: additional,
	}, nil
}

func isUpToDate(ctx context.Context, logger *logrus.Entry, repo, dir, commit string) bool {
	logger = logger.WithField("repo", repo)
	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "merge-base", "--is-ancestor", commit, "main",
	), dir)); err == nil {
		logger.WithField("commit", commit).Info("branch already contains target commit, nothing to do")
		return true
	}
	return false
}

var upstreamCommitRegex = regexp.MustCompile(`^UPSTREAM: (revert: )?(([\w.-]+/[\w-.-]+)?: )?(\d+:|<carry>:|<drop>:)`)

func detectCarryCommits(ctx context.Context, logger *logrus.Entry, repo, dir, commit string, mode flags.FetchMode) ([]internal.Commit, error) {
	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "fetch", upstreamRemote(repo, mode), commit,
	), dir)); err != nil {
		return nil, err
	}

	var mergeBase string
	{
		mergeBaseRaw, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "merge-base", "main", "FETCH_HEAD",
		), dir))
		if err != nil {
			return nil, err
		}
		mergeBase = strings.TrimSpace(mergeBaseRaw)
	}

	var downstreamCommits []internal.Commit
	{
		rawCommits, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "log", mergeBase+"..main",
			"--ancestry-path", mergeBase,
			"--no-merges", "--reverse", "--quiet",
			internal.PrettyFormat,
		), dir))
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(rawCommits, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			info, err := internal.ParseFormat(line)
			if err != nil {
				return nil, err
			}
			logger = logger.WithFields(logrus.Fields{
				"commit":  info.Hash,
				"message": info.Message,
			})
			messageMatches := upstreamCommitRegex.FindStringSubmatch(info.Message)
			if len(messageMatches) == 0 || len(messageMatches[0]) == 0 {
				return nil, fmt.Errorf("unexpected commit message: %s", info.Message)
			}

			// TODO: handle reverts, what else?
			match := strings.Trim(messageMatches[4], "<>:")
			switch match {
			case "drop":
				logger.Info("dropping commit")
				continue
			case "carry":
				logger.Info("carrying commit")
				downstreamCommits = append(downstreamCommits, info)
			default:
				logger.Info("investigating cherry-picked PR")
				// The UPSTREAM: 1234: format only tells us the upstream pull request that was cherry-picked. Unfortunately,
				// there's no great way to figure out if that pull request is still something we need to carry or if it's part
				// of the newer version we're pulling in. The GitHub synthetic ref pull/1234/head gives us the commits that
				// make up the pull request, but merge strategies other than "merge" will edit those commits - all the v1
				// repos use such a strategy. We could reach out to the GitHub API, but that's expensive and time consming.

				// Instead, we know that (today) all the v1 repos we're managing use a strategy that renames the commit
				// to be sufffixed with the pull request "(#1234)", so we can check to see if such a commit exists upstream.
				// This is not bullet-proof, but the failure mode is that we will try to cherry-pick a commit that does not
				// need to be there and will either apply as a benign empty commit or fail to apply - both acceptable outcomes.
				// We won't silently do the wrong thing.

				rawMatches, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
					"git", "log", "--pretty=format:%H", "--grep", fmt.Sprintf("(#%s)", match), commit,
				), dir))
				if err != nil {
					return nil, err
				}

				if len(strings.TrimSpace(rawMatches)) == 0 {
					logger.Info("cherry-picked PR needs to be carried")
					downstreamCommits = append(downstreamCommits, info)
				}
			}
		}
	}
	return downstreamCommits, nil
}

func applyConfig(ctx context.Context, logger *logrus.Entry, org, repo, branch, dir string, config Config, commitArgs []string) error {
	// first, get us to the upstream target
	for _, cmd := range [][]string{
		{"git", "checkout", branch},
		{"git", "branch", "synchronize", "--force", config.Target.Hash},
		{"git", "checkout", "synchronize"},
		append([]string{"git", "merge", "--strategy", "ours", branch}, commitArgs...),
	} {
		if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			cmd[0], cmd[1:]...,
		), dir)); err != nil {
			return err
		}
	}

	// then, cherry-pick the additional bits
	for _, commit := range config.Additional {
		if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "cherry-pick", commit.Hash,
		), dir)); err != nil {
			return err
		}
	}

	// finally, apply our generated patches on top
	for _, cmd := range []*exec.Cmd{
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "tidy",
		), dir), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "vendor",
		), dir), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "verify",
		), dir), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "tidy",
		), filepath.Join(dir, "openshift")), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "vendor",
		), filepath.Join(dir, "openshift")), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "verify",
		), filepath.Join(dir, "openshift")), os.Environ()...),
		// git commit with filenames does not require staging, but since these repos
		// choose to put vendor in gitignore, we need git add --force to stage those
		internal.WithDir(exec.CommandContext(ctx,
			"git", "add", "--force",
			"vendor", "go.mod", "go.sum",
			"openshift/vendor", "openshift/go.mod", "openshift/go.sum",
		), dir),
		internal.WithDir(exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"vendor", "go.mod", "go.sum",
				"openshift/vendor", "openshift/go.mod", "openshift/go.sum",
				"--message", "UPSTREAM: <drop>: go mod vendor"},
				commitArgs...)...,
		), dir),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"make", "-f", "openshift/Makefile", "manifests",
		), dir), os.Environ()...),
		internal.WithDir(exec.CommandContext(ctx,
			"git", "add", "--force",
			"openshift/manifests",
		), dir),
		internal.WithDir(exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"openshift/manifests",
				"--message", "UPSTREAM: <drop>: generate manifests"},
				commitArgs...)...,
		), dir),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"rm", "-rf", ".github",
		), dir), os.Environ()...),
		internal.WithDir(exec.CommandContext(ctx,
			"git", "add", "--force",
			".github",
		), dir),
		internal.WithDir(exec.CommandContext(ctx,
			"git", append([]string{"commit",
				".github",
				"--message", "UPSTREAM: <drop>: remove upstream GitHub configuration"},
				commitArgs...)...,
		), dir),
	} {
		if _, err := internal.RunCommand(logger, cmd); err != nil {
			return err
		}
	}

	return writeCommitCheckerFile(ctx, logger, org, repo, branch, config.Target.Hash, dir, commitArgs)
}

func rewriteGoMod(ctx context.Context, logger *logrus.Entry, dir string, commits map[string]string, commitArgs []string) error {
	env := append(os.Environ(), "GOPROXY=direct")
	for name, commit := range commits {
		if _, err := internal.RunCommand(logger, internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "edit", "-replace", fmt.Sprintf("github.com/operator-framework/%s=github.com/openshift/operator-framework-%s@%s", name, name, commit),
		), dir), os.Environ()...)); err != nil {
			return err
		}
		for _, cmd := range []*exec.Cmd{
			exec.CommandContext(ctx, "go", "mod", "tidy"),
			exec.CommandContext(ctx, "go", "mod", "vendor"),
			exec.CommandContext(ctx, "go", "mod", "verify"),
		} {
			if _, err := internal.RunCommand(logger, internal.WithEnv(internal.WithDir(cmd, dir), env...)); err != nil {
				return err
			}
		}
	}

	for _, cmd := range []*exec.Cmd{
		// git commit with filenames does not require staging, but since these repos
		// choose to put vendor in gitignore, we need git add --force to stage those
		internal.WithDir(exec.CommandContext(ctx,
			"git", "add", "--force",
			"vendor", "go.mod", "go.sum",
		), dir),
		exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"vendor", "go.mod", "go.sum",
				"--message", "UPSTREAM: <drop>: rewrite go mod"},
				commitArgs...)...,
		),
	} {
		if _, err := internal.RunCommand(logger, internal.WithDir(cmd, dir)); err != nil {
			return err
		}
	}
	return nil
}

func writeCommitCheckerFile(ctx context.Context, logger *logrus.Entry, org, repo, branch, expectedMergeBase, dir string, commitArgs []string) error {
	// TODO: move the upstream commit-checker code out of `main` package so we can import this and the regex
	var config = struct {
		// UpstreamOrg is the organization of the upstream repository
		UpstreamOrg string `json:"upstreamOrg,omitempty"`
		// UpstreamRepo is the repo name of the upstream repository
		UpstreamRepo string `json:"upstreamRepo,omitempty"`
		// UpstreamBranch is the branch from the upstream repository we're tracking
		UpstreamBranch string `json:"upstreamBranch,omitempty"`
		// ExpectedMergeBase is the latest commit from the upstream that is expected to be present in this downstream
		ExpectedMergeBase string `json:"expectedMergeBase,omitempty"`
	}{
		UpstreamOrg:       org,
		UpstreamRepo:      repo,
		UpstreamBranch:    branch,
		ExpectedMergeBase: expectedMergeBase,
	}

	raw, err := yaml.Marshal(&config)
	if err != nil {
		return fmt.Errorf("failed to marshal commit checker config: %w", err)
	}

	if err := os.WriteFile(filepath.Join(dir, "commitchecker.yaml"), raw, 0666); err != nil {
		return fmt.Errorf("failed to write commit checker config: %w", err)
	}

	for _, cmd := range []*exec.Cmd{
		// git commit with filenames does not require staging, but since these repos
		// choose to put vendor in gitignore, we need git add --force to stage those
		exec.CommandContext(ctx,
			"git", "add", "--force",
			"commitchecker.yaml",
		),
		exec.CommandContext(ctx,
			"git", append([]string{"commit",
				"commitchecker.yaml",
				"--message", "UPSTREAM: <drop>: configure the commit-checker"},
				commitArgs...)...,
		),
	} {
		if _, err := internal.RunCommand(logger, internal.WithDir(cmd, dir)); err != nil {
			return err
		}
	}
	return nil
}
