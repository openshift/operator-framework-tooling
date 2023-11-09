package main

import (
	"context"
	"flag"
	"os"
	"os/signal"

	v1 "github.com/openshift/operator-framework-tooling/pkg/v1"
	"github.com/sirupsen/logrus"
)

func main() {
	logger := logrus.New()
	opts := v1.DefaultOptions()
	opts.Bind(flag.CommandLine)
	flag.Parse()

	if err := opts.Validate(); err != nil {
		logger.WithError(err).Fatal("invalid options")
	}

	logLevel, _ := logrus.ParseLevel(opts.LogLevel)
	logger.SetLevel(logLevel)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	if err := v1.Run(ctx, logger, opts); err != nil {
		logrus.WithError(err).Fatal("failed to execute")
	}
}
