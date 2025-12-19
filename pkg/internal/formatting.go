package internal

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/cmd/generic-autobumper/bumper"
	"k8s.io/test-infra/prow/config/secret"
)

type Commit struct {
	Date    time.Time `json:"date"`
	Hash    string    `json:"hash,omitempty"`
	Author  string    `json:"author,omitempty"`
	Message string    `json:"message,omitempty"`
	Repo    string    `json:"repo,omitempty"`
}

func Info(ctx context.Context, logger *logrus.Entry, sha, dir string) (Commit, error) {
	infoCmd := WithDir(exec.CommandContext(ctx,
		"git", "show",
		sha,
		PrettyFormat,
		"--quiet",
	), dir)
	stdout, stderr := bytes.Buffer{}, bytes.Buffer{}
	infoCmd.Stdout = bumper.HideSecretsWriter{Delegate: &stdout, Censor: secret.Censor}
	infoCmd.Stderr = bumper.HideSecretsWriter{Delegate: &stderr, Censor: secret.Censor}
	logger.WithField("command", infoCmd.String()).Debug("running command")
	if err := infoCmd.Run(); err != nil {
		return Commit{}, fmt.Errorf("failed to run command: %s %s: %w", stdout.String(), stderr.String(), err)
	}
	return ParseFormat(stdout.String())
}

const PrettyFormat = "--pretty=format:%H\u00A0%cI\u00A0%an\u00A0%s"

func ParseFormat(format string) (Commit, error) {
	parts := strings.Split(format, "\u00A0")
	if len(parts) != 4 {
		return Commit{}, fmt.Errorf("incorrect parts from git output: %v", format)
	}
	committedTime, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return Commit{}, fmt.Errorf("invalid time %s: %w", parts[1], err)
	}
	return Commit{
		Hash:    parts[0],
		Date:    committedTime,
		Author:  parts[2],
		Message: parts[3],
	}, nil
}

func Table(logger *logrus.Logger, commits []Commit, repoBase string) {
	writer := tabwriter.NewWriter(bumper.HideSecretsWriter{Delegate: os.Stdout, Censor: secret.Censor}, 0, 4, 2, ' ', 0)
	for _, commit := range commits {
		if _, err := fmt.Fprintln(writer, commit.Date.Format(time.DateTime)+"\t"+repoBase+commit.Repo+"\t", commit.Hash+"\t"+commit.Author+"\t"+commit.Message); err != nil {
			logger.WithError(err).Error("failed to write output")
		}
	}
	if err := writer.Flush(); err != nil {
		logger.WithError(err).Error("failed to flush output")
	}
}

func GetBody(commits []Commit, assign []string) string {
	lines := []string{
		"The staging/ and vendor/ directories have been synchronized from the upstream repositories, pulling in the following commits:",
		"",
		"| Date | Commit | Author | Message |",
		"| -    | -      | -      | -       |",
	}
	for _, commit := range commits {
		lines = append(
			lines,
			fmt.Sprintf("|%s|[operator-framework/%s@%s](https://github.com/operator-framework/%s/commit/%s)|%s|%s|",
				commit.Date.Format(time.DateTime),
				commit.Repo,
				commit.Hash[0:7],
				commit.Repo,
				commit.Hash,
				commit.Author,
				commit.Message,
			),
		)
	}
	lines = append(lines, "", "This pull request is expected to merge without any human intervention. If tests are failing here, changes must land upstream to fix any issues so that future downstreaming efforts succeed.", "")
	for _, who := range assign {
		lines = append(lines, fmt.Sprintf("/cc @%s", who))
	}

	body := strings.Join(lines, "\n")

	if len(body) >= 65536 {
		body = body[:65530] + "..."
	}

	return body
}

// ExtractJiraTickets extracts JIRA tickets from full commit messages based on the provided project names.
// Returns a sorted, deduplicated list of JIRA tickets found in the format PROJECT-NUMBER.
func ExtractJiraTickets(ctx context.Context, logger *logrus.Entry, commits []Commit, jiraProjects []string, dir string) []string {
	if len(jiraProjects) == 0 || len(commits) == 0 {
		return nil
	}

	// Build regex pattern from jira projects: (?:PROJECT1|PROJECT2)-\d+
	projectPattern := strings.Join(jiraProjects, "|")
	pattern := fmt.Sprintf(`\b(?:%s)-\d+\b`, projectPattern)
	jiraRegex := regexp.MustCompile(pattern)

	ticketSet := make(map[string]bool)
	for i := range commits {
		// Get full commit message body
		fullMessage, err := RunCommand(logger, WithDir(exec.CommandContext(ctx,
			"git", "show", "-s", "--format=%B", commits[i].Hash,
		), dir))
		if err != nil {
			logger.WithError(err).WithField("commit", commits[i].Hash).Warn("failed to fetch full commit message, using subject line only")
			fullMessage = commits[i].Message
		}

		matches := jiraRegex.FindAllString(fullMessage, -1)
		for _, match := range matches {
			ticketSet[match] = true
		}
	}

	if len(ticketSet) == 0 {
		return nil
	}

	// Convert map to sorted slice
	tickets := make([]string, 0, len(ticketSet))
	for ticket := range ticketSet {
		tickets = append(tickets, ticket)
	}
	sort.Strings(tickets)

	return tickets
}

func GetBodyV1(targetList []Commit, commits []Commit, assign []string, jiraTickets []string) string {
	lines := []string{}

	// Prepend JIRA tickets if provided
	if len(jiraTickets) > 0 {
		lines = append(lines, "**JIRA Tickets:**")
		lines = append(lines, "")
		for _, ticket := range jiraTickets {
			lines = append(lines, fmt.Sprintf("- %s", ticket))
		}
		lines = append(lines, "")
	}

	lines = append(lines,
		"The downstream repository has been updated with the following following upstream commits:",
		"",
		"| Date | Commit | Author | Message |",
		"| -    | -      | -      | -       |",
	)
	for _, commit := range targetList {
		lines = append(lines, fmt.Sprintf("|%s|[operator-framework/%s@%s](https://github.com/operator-framework/%s/commit/%s)|%s|%s|",
			commit.Date.Format(time.DateTime),
			commit.Repo,
			commit.Hash[0:7],
			commit.Repo,
			commit.Hash,
			commit.Author,
			commit.Message,
		))
	}
	lines = append(lines,
		"",
		"The `vendor/` directory has been updated and the following commits were carried:",
		"",
		"| Date | Commit | Author | Message |",
		"| -    | -      | -      | -       |",
	)
	for _, commit := range commits {
		lines = append(
			lines,
			fmt.Sprintf("|%s|[openshift/operator-framework-%s@%s](https://github.com/openshift/operator-framework-%s/commit/%s)|%s|%s|",
				commit.Date.Format(time.DateTime),
				commit.Repo,
				commit.Hash[0:7],
				commit.Repo,
				commit.Hash,
				commit.Author,
				commit.Message,
			),
		)
	}
	lines = append(lines, "", "This pull request is expected to merge without any human intervention. If tests are failing here, changes must land upstream to fix any issues so that future downstreaming efforts succeed.", "")
	for _, who := range assign {
		lines = append(lines, fmt.Sprintf("/cc @%s", who))
	}

	body := strings.Join(lines, "\n")

	if len(body) >= 65536 {
		body = body[:65530] + "..."
	}

	return html.EscapeString(body)
}
