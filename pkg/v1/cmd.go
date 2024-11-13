package v1

import (
	"bufio"
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

	TideMergeMethodMergeLabel = "tide/merge-method-merge"
	KindSyncLabel             = "kind/sync"
)

func DefaultOptions() Options {
	opts := Options{
		Options: flags.DefaultOptions(),
	}
	opts.Options.PRBaseBranch = defaultBranch
	return opts
}

type Options struct {
	operatorControllerDir string
	catalogDDir           string

	pauseOnCherryPickError  bool
	printPullRequestComment bool
	forceRemerge            bool

	dropCommits     string
	listDropCommits []string

	flags.Options
}

func (o *Options) Bind(fs *flag.FlagSet) {
	fs.StringVar(&o.operatorControllerDir, "operator-controller-dir", o.operatorControllerDir, "Directory for operator-controller repository.")
	fs.StringVar(&o.catalogDDir, "catalogd-dir", o.catalogDDir, "Directory for catalogd repository.")
	fs.BoolVar(&o.pauseOnCherryPickError, "pause-on-cherry-pick-error", o.pauseOnCherryPickError, "When an error occurs during cherry-pick, pause to allow the user to fix.")
	fs.BoolVar(&o.printPullRequestComment, "print-pull-request-comment", o.printPullRequestComment, "During synchonize mode, print out the pull request comment (for pasting into a PR).")
	fs.BoolVar(&o.forceRemerge, "force-remerge", o.forceRemerge, "When synchonizing, force a merge of the upstream branch again.")
	fs.StringVar(&o.dropCommits, "drop-commits", o.dropCommits, "Comma-separated list of carry commit SHAs to drop.")

	o.Options.Bind(fs)
}

func (o *Options) Validate() error {
	if err := o.Options.Validate(); err != nil {
		return err
	}

	for name, val := range map[string]string{
		"operator-controller": o.operatorControllerDir,
		"catalogd":            o.catalogDDir,
	} {
		if val == "" {
			return fmt.Errorf("--%s-dir is required", name)
		}
	}

	if o.dropCommits != "" {
		o.listDropCommits = strings.Split(o.dropCommits, ",")
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
		commits, err = detectNewCommits(ctx, logger.WithField("phase", "detect"), directories, opts)
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

	// Get the tools the repo needs via bingo
	if err := internal.RunBingo(ctx, logger.WithField("phase", "bingo")); err != nil {
		logger.WithError(err).Fatal("failed to setup tools via bingo")
	}

	cherryPickAll := func() {
		if err := internal.SetCommitter(ctx, logger.WithField("phase", "setup"), opts.GitName, opts.GitEmail); err != nil {
			logger.WithError(err).Fatal("failed to set committer")
		}
		for repo, config := range commits {
			commitLogger := logger.WithField("repo", repo)
			if err := applyConfig(ctx, commitLogger, "operator-framework", repo, "main", directories[repo], config, opts.GitCommitArgs(), opts.pauseOnCherryPickError, opts.Options.DelayManifestGeneration); err != nil {
				logger.WithError(err).Fatal("failed to merge to upstream")
			}
		}
		// we need the operator-framework-operator-controller go.mod to point to the downstream libraries
		// that we're synchronizing above, but we can't have replace directives in the go.mod until the
		// downstream repositories have the desired git state already published. Therefore, only if we
		// found that the repos are up-to-date (they are not in the commits map) can we do the replacing.
		otherCommits := map[string]string{}
		for _, repo := range []string{"catalogd"} {
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

	labelsToAdd := []string{
		// The repos is set to use rebase merge method for making it easier to programmatically
		// determine the commits which need to be carried. But the sync PR itself need to use merge.
		// By adding this label we instruct tide to merge instead of using the default behaviour.
		TideMergeMethodMergeLabel,
		KindSyncLabel,
	}

	switch flags.Mode(opts.Mode) {
	case flags.Summarize:
		for repo, info := range commits {
			fmt.Printf("openshift/operator-framework-%s: updating to:\n", repo)
			internal.Table(logger, []internal.Commit{info.Target}, "operator-framework/")
			fmt.Println(" + additional commits to cherry-pick on top:")
			internal.Table(logger, info.Additional, "openshift/operator-framework-")
			fmt.Println()
		}
	case flags.Synchronize:
		cherryPickAll()
		if opts.printPullRequestComment {
			for repo, config := range commits {
				s := fmt.Sprintf("For repo openshift/operator-framework-%s", repo)
				fmt.Println(strings.Repeat("=", len(s)))
				fmt.Println(s)
				fmt.Println(strings.Repeat("=", len(s)))
				s = internal.GetBodyV1(config.Target, config.Additional, strings.Split(opts.Assign, ","))
				fmt.Println(s)
				for _, label := range labelsToAdd {
					fmt.Printf("/label %s\n", label)
				}
			}
		}
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

			if opts.SelfApprove {
				logger.Infof("Self-approving PR by adding the %q and %q labels", labels.Approved, labels.LGTM)
				labelsToAdd = append(labelsToAdd, labels.Approved, labels.LGTM)
			}
			if err := bumper.UpdatePullRequestWithLabels(gc, opts.GithubOrg, fork, title,
				internal.GetBodyV1(config.Target, config.Additional, strings.Split(opts.Assign, ",")),
				opts.GithubLogin+":"+remoteBranch, opts.PRBaseBranch, remoteBranch, true, labelsToAdd, opts.DryRun); err != nil {
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

var syntheticVersionRegex = regexp.MustCompile(`[^-]+-(?:[0-9]+\.)[0-9]{14}-([0-9a-f]+)`)

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

func detectNewCommits(ctx context.Context, logger *logrus.Entry, directories map[string]string, opts Options) (map[string]Config, error) {
	mode := flags.FetchMode(opts.FetchMode)
	if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "fetch", "--tags", upstreamRemote("operator-controller", mode),
	), directories["operator-controller"])); err != nil {
		return nil, fmt.Errorf("failed to fetch upstream: %w", err)
	}

	target := map[string]Config{}
	config, err, upToDate := detectNewOperatorControllerCommits(ctx, logger, directories["operator-controller"], opts)
	if err != nil {
		return nil, err
	}
	if config != nil {
		config.Target.Repo = "operator-controller"
		target["operator-controller"] = *config
	}

	if !upToDate {
		if _, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
			"git", "checkout", target["operator-controller"].Target.Hash,
		), directories["operator-controller"])); err != nil {
			return nil, fmt.Errorf("failed to check out upstream target: %w", err)
		}
	}

	for _, name := range []string{"catalogd"} {
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
		commit.Repo = name
		if err != nil {
			return nil, fmt.Errorf("failed to determine commit info: %w", err)
		}
		if !opts.forceRemerge && isUpToDate(ctx, logger, name, directories[name], commit.Hash) {
			continue
		}
		additional, err := detectCarryCommits(ctx, logger, name, directories[name], commit.Hash, opts)
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

func detectNewOperatorControllerCommits(ctx context.Context, logger *logrus.Entry, dir string, opts Options) (*Config, error, bool) {
	commitSha, err := internal.RunCommand(logger, internal.WithDir(exec.CommandContext(ctx,
		"git", "rev-parse", "FETCH_HEAD",
	), dir))
	if err != nil {
		return nil, fmt.Errorf("failed to parse upstream HEAD: %w", err), false
	}
	commitSha = strings.TrimSpace(commitSha)
	logger.WithFields(logrus.Fields{"repo": "operator-controller", "commit": commitSha}).Info("resolved latest commit")
	commit, err := internal.Info(ctx, logger, commitSha, dir)
	if err != nil {
		return nil, fmt.Errorf("failed to determine commit info: %w", err), false
	}
	if isUpToDate(ctx, logger, "operator-controller", dir, commit.Hash) {
		return nil, nil, true
	}
	additional, err := detectCarryCommits(ctx, logger, "operator-controller", dir, commit.Hash, opts)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve additional commits: %w", err), false
	}
	return &Config{
		Target:     commit,
		Additional: additional,
	}, nil, false
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

func detectCarryCommits(ctx context.Context, logger *logrus.Entry, repo, dir, commit string, opts Options) ([]internal.Commit, error) {
	mode := flags.FetchMode(opts.FetchMode)
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
			info.Repo = repo
			logger = logger.WithFields(logrus.Fields{
				"commit":  info.Hash,
				"message": info.Message,
			})
			messageMatches := upstreamCommitRegex.FindStringSubmatch(info.Message)
			if len(messageMatches) == 0 || len(messageMatches[0]) == 0 {
				return nil, fmt.Errorf("unexpected commit message: %s", info.Message)
			}

			drop := ""
			for _, c := range opts.listDropCommits {
				if strings.HasPrefix(info.Hash, c) {
					drop = c
					break
				}
			}
			if drop != "" {
				logger.WithField("option=drop-commits", drop).Info("dropping commit due to option")
				continue
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

func applyConfig(ctx context.Context, logger *logrus.Entry, org, repo, branch, dir string, config Config, commitArgs []string, pauseOnCherryPickError, delayManifestGeneration bool) error {
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
		cherryPickCommands := []*exec.Cmd{
			internal.WithDir(exec.CommandContext(ctx,
				"git", "cherry-pick", commit.Hash,
			), dir),
		}
		goModCommands := []*exec.Cmd{
			internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
				"go", "mod", "tidy",
			), filepath.Join(dir, "openshift")), os.Environ()...),
			internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
				"go", "mod", "vendor",
			), filepath.Join(dir, "openshift")), os.Environ()...),
			internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
				"go", "mod", "verify",
			), filepath.Join(dir, "openshift")), os.Environ()...),
		}
		generateManifestsCommands := []*exec.Cmd{
			internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
				"make", "-f", "openshift/Makefile", "manifests",
			), dir), os.Environ()...),
		}
		cleanManifestsCommands := []*exec.Cmd{
			internal.WithDir(exec.CommandContext(ctx,
				"git", "rm", "-rf", "--ignore-unmatch", "openshift/manifests",
			), dir),
		}

		commitCommands := []*exec.Cmd{
			internal.WithDir(exec.CommandContext(ctx,
				"git", "add", "--force", "openshift/.",
			), dir),
			// git commit with filenames does not require staging, but since these repos
			// choose to put vendor in gitignore, we need git add --force to stage those
			internal.WithDir(exec.CommandContext(ctx,
				"git", append([]string{"commit", "openshift/.",
					"--amend",
					"--no-edit",
				}, commitArgs...)...,
			), dir),
		}

		commands := goModCommands
		if delayManifestGeneration {
			commands = append(commands, cleanManifestsCommands...)
		} else {
			commands = append(commands, generateManifestsCommands...)
		}
		commands = append(commands, commitCommands...)

		// Cherry picking has special error handling
		for _, cmd := range cherryPickCommands {
			if msg, err := internal.RunCommand(logger, cmd); err != nil {
				if pauseOnCherryPickError {
					fmt.Printf("Error during cherry-pick:\n%s", msg)
					fmt.Print("Please resolve the cherry-pick conflict. <ENTER> to continue, 'q' to terminate>")
					text, ioErr := bufio.NewReader(os.Stdin).ReadString('\n')
					if ioErr != nil || strings.TrimSpace(text) == "q" {
						return err
					}
				} else {
					return err
				}
			}
		}

		// Run the rest of the commands
		for _, cmd := range commands {
			if _, err := internal.RunCommand(logger, cmd); err != nil {
				return err
			}
		}
	}

	extraVendor := map[string][]string{
		"operator-controller": {"testdata/push", "testdata/registry"},
	}

	generatedPatches := []*exec.Cmd{
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "tidy",
		), dir), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "vendor",
		), dir), os.Environ()...),
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"go", "mod", "verify",
		), dir), os.Environ()...),
	}

	addFiles := []string{"vendor", "go.mod", "go.sum"}
	if vendorDirs, ok := extraVendor[repo]; ok {
		for _, vd := range vendorDirs {
			generatedPatches = append(generatedPatches, []*exec.Cmd{
				internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
					"go", "mod", "tidy",
				), filepath.Join(dir, vd)), os.Environ()...),
				internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
					"go", "mod", "vendor",
				), filepath.Join(dir, vd)), os.Environ()...),
				internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
					"go", "mod", "verify",
				), filepath.Join(dir, vd)), os.Environ()...)}...)
			addFiles = append(addFiles, []string{
				filepath.Join(vd, "vendor"),
				filepath.Join(vd, "go.mod"),
				filepath.Join(vd, "go.sum"),
			}...)
		}
	}

	generatedPatches = append(generatedPatches, []*exec.Cmd{
		// git commit with filenames does not require staging, but since these repos
		// choose to put vendor in gitignore, we need git add --force to stage those
		internal.WithDir(exec.CommandContext(ctx,
			"git", append([]string{"add", "--force"}, addFiles...)...,
		), dir),
		internal.WithDir(exec.CommandContext(ctx,
			"git", append(append([]string{"commit", "--message", "UPSTREAM: <drop>: go mod vendor"},
				addFiles...), commitArgs...)...,
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
	}...)

	commitManifests := []*exec.Cmd{
		internal.WithEnv(internal.WithDir(exec.CommandContext(ctx,
			"make", "-f", "openshift/Makefile", "manifests",
		), dir), os.Environ()...),
		internal.WithDir(exec.CommandContext(ctx,
			"git", "add", "--force", "openshift/manifests",
		), dir),
		// git commit with filenames does not require staging, but since these repos
		// choose to put vendor in gitignore, we need git add --force to stage those
		internal.WithDir(exec.CommandContext(ctx,
			"git", append([]string{"commit", "openshift/manifests",
				"--message", "UPSTREAM: <drop>: Generate manifests",
			}, commitArgs...)...,
		), dir),
	}

	commands := generatedPatches
	if delayManifestGeneration {
		commands = append(commands, commitManifests...)
	}

	// finally, apply our generated patches on top
	for _, cmd := range commands {
		if _, err := internal.RunCommand(logger, cmd); err != nil {
			return err
		}
	}

	return writeCommitCheckerFile(ctx, logger, org, repo, branch, config.Target.Hash, dir, commitArgs)
}

func rewriteGoMod(ctx context.Context, logger *logrus.Entry, dir string, commits map[string]string, commitArgs []string) error {
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
			if _, err := internal.RunCommand(logger, internal.WithEnv(internal.WithDir(cmd, dir), os.Environ()...)); err != nil {
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
			if strings.Contains(err.Error(), "nothing to commit, working tree clean") {
				logger.Info("no go.mod changes to commit, continuing")
				return nil
			}
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
