package flags

import (
	"flag"
	"fmt"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/flagutil"
)

type Mode string

const (
	Summarize   Mode = "summarize"
	Synchronize Mode = "synchronize"
	Publish     Mode = "publish"
)

type FetchMode string

const (
	HTTPS FetchMode = "https"
	SSH   FetchMode = "ssh"
)

const (
	GithubOrg   = "openshift"
	GithubLogin = "openshift-bot"

	RuntimePRAssignee   = "openshift/openshift-team-operator-runtime"
	EcosystemPRAssignee = "openshift/openshift-team-operator-ecosystem"
	DefaultPRAssignee   = RuntimePRAssignee + "," + EcosystemPRAssignee

	DefaultBaseBranch = "master"
)

type Options struct {
	CommitFileOutput string
	CommitFileInput  string
	Mode             string
	LogLevel         string
	FetchMode        string

	DryRun       bool
	GithubLogin  string
	GithubOrg    string
	GithubRepo   string
	GitName      string
	GitEmail     string
	GitSignoff   bool
	Assign       string
	SelfApprove  bool
	PRBaseBranch string

	DelayManifestGeneration bool
	DelayGoMod              bool

	flagutil.GitHubOptions
}

func DefaultOptions() Options {
	return Options{
		Mode:                    string(Summarize),
		LogLevel:                logrus.InfoLevel.String(),
		FetchMode:               string(SSH),
		DryRun:                  true,
		GithubLogin:             GithubLogin,
		GithubOrg:               GithubOrg,
		GitSignoff:              false,
		Assign:                  DefaultPRAssignee,
		SelfApprove:             false,
		PRBaseBranch:            DefaultBaseBranch,
		DelayManifestGeneration: false,
		DelayGoMod:              false,
	}
}

func (o *Options) Bind(fs *flag.FlagSet) {
	fs.StringVar(&o.Mode, "mode", o.Mode, fmt.Sprintf("Operation Mode. One of %s", []Mode{Summarize, Synchronize, Publish}))
	fs.StringVar(&o.CommitFileOutput, "commits-output", o.CommitFileInput, "File to write commits data to after resolving what needs to be synced.")
	fs.StringVar(&o.CommitFileInput, "commits-input", o.CommitFileOutput, "File to read commits data from in order to drive sync process.")
	fs.StringVar(&o.LogLevel, "log-level", o.LogLevel, "Logging level.")
	fs.StringVar(&o.FetchMode, "fetch-mode", o.FetchMode, "Method to use for fetching from git remotes.")

	fs.BoolVar(&o.DryRun, "dry-run", o.DryRun, "Whether to actually create the pull request with github client")
	fs.StringVar(&o.GithubLogin, "github-login", o.GithubLogin, "The GitHub username to use.")
	fs.StringVar(&o.GithubOrg, "org", o.GithubOrg, "The downstream GitHub org name.")
	fs.StringVar(&o.GithubRepo, "repo", o.GithubRepo, "The downstream GitHub repository name.")
	fs.StringVar(&o.GitName, "git-name", o.GitName, "The name to use on the git commit. Requires --git-email. If not specified, uses the system default.")
	fs.StringVar(&o.GitEmail, "git-email", o.GitEmail, "The email to use on the git commit. Requires --git-name. If not specified, uses the system default.")
	fs.BoolVar(&o.GitSignoff, "git-signoff", o.GitSignoff, "Whether to signoff the commit. (https://git-scm.com/docs/git-commit#Documentation/git-commit.txt---signoff)")
	fs.StringVar(&o.Assign, "assign", o.Assign, "The comma-delimited set of github usernames or group names to assign the created pull request to.")
	fs.BoolVar(&o.SelfApprove, "self-approve", o.SelfApprove, "Self-approve the PR by adding the `approved` and `lgtm` labels. Requires write permissions on the repo.")
	fs.StringVar(&o.PRBaseBranch, "pr-base-branch", o.PRBaseBranch, "The base branch to use for the pull request.")
	fs.BoolVar(&o.DelayManifestGeneration, "delay-manifest-generation", o.DelayManifestGeneration, "Delay manifest generation until the end.")
	fs.BoolVar(&o.DelayGoMod, "delay-go-mod", o.DelayGoMod, "Delay running 'go mod' commands until the end.")
	o.GitHubOptions.AddFlags(fs)
	o.GitHubOptions.AllowAnonymous = true
}

func (o *Options) Validate() error {
	switch Mode(o.Mode) {
	case Summarize, Synchronize, Publish:
	default:
		return fmt.Errorf("--mode must be one of %v", []Mode{Summarize, Synchronize, Publish})
	}

	switch FetchMode(o.FetchMode) {
	case SSH, HTTPS:
	default:
		return fmt.Errorf("--fetch-mode must be one of %v", []FetchMode{HTTPS, SSH})
	}

	if _, err := logrus.ParseLevel(o.LogLevel); err != nil {
		return fmt.Errorf("--log-level invalid: %w", err)
	}

	if Mode(o.Mode) == Publish {
		if o.GithubLogin == "" {
			return fmt.Errorf("--github-login is mandatory")
		}
		if (o.GitEmail == "") != (o.GitName == "") {
			return fmt.Errorf("--git-name and --git-email must be specified together")
		}
		if o.Assign == "" {
			return fmt.Errorf("--assign is mandatory")
		}

		if err := o.GitHubOptions.Validate(o.DryRun); err != nil {
			return err
		}
	}
	return nil
}

func (o *Options) GitCommitArgs() []string {
	var commitArgs []string
	if o.GitSignoff {
		commitArgs = append(commitArgs, "--signoff")
	}
	return commitArgs
}
