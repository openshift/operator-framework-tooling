package internal

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"

	"github.com/sirupsen/logrus"
	"k8s.io/test-infra/prow/cmd/generic-autobumper/bumper"
	"k8s.io/test-infra/prow/config/secret"
)

func SetCommitter(ctx context.Context, logger *logrus.Entry, name string, email string) error {
	for field, value := range map[string]string{
		"user.name":  name,
		"user.email": email,
	} {
		output, err := RunCommand(logger, exec.CommandContext(ctx,
			"git", "config",
			"--get", field,
		))
		if err != nil {
			return err
		}
		if len(output) == 0 {
			_, err := RunCommand(logger, exec.CommandContext(ctx,
				"git", "config",
				"--add", field, value,
			))
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func RunCommand(logger *logrus.Entry, cmd *exec.Cmd) (string, error) {
	output := bytes.Buffer{}
	cmd.Stdout = bumper.HideSecretsWriter{Delegate: &output, Censor: secret.Censor}
	cmd.Stderr = bumper.HideSecretsWriter{Delegate: &output, Censor: secret.Censor}
	logger = logger.WithFields(logrus.Fields{"command": cmd.String(), "dir": cmd.Dir})
	logger.Debug("running command")
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("failed to run command: %s: %w", output.String(), err)
	}
	logger.WithField("output", output.String()).Debug("ran command")
	return output.String(), nil
}

func WithEnv(command *exec.Cmd, env ...string) *exec.Cmd {
	command.Env = append(command.Env, env...)
	return command
}

func WithDir(command *exec.Cmd, dir string) *exec.Cmd {
	command.Dir = dir
	return command
}
