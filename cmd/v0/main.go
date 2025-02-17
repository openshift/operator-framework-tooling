package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	v0 "github.com/openshift/operator-framework-tooling/pkg/v0"
	"github.com/sirupsen/logrus"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	logger := logrus.New()
	opts := v0.DefaultOptions(ctx, logger)
	opts.Bind(flag.CommandLine)
	flag.Parse()

	if err := opts.Validate(); err != nil {
		logger.WithError(err).Fatal("invalid options")
	}

	logLevel, _ := logrus.ParseLevel(opts.LogLevel)
	logger.SetLevel(logLevel)

	if err := v0.Run(ctx, logger, opts); err != nil {
		logrus.WithError(err).Fatal("failed to execute")
	}
}
