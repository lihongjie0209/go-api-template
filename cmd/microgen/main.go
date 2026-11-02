package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lihongjie0209/go-api-template/internal/microgencli"
	"github.com/lihongjie0209/go-api-template/internal/observability"
)

var errMicrogenCommandFailed = errors.New("microgen_command_failed")

func safeCommandError(err error) error {
	return observability.SafeError(err, errMicrogenCommandFailed)
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := microgencli.NewCommand()
	command.SetOut(os.Stdout)
	command.SetErr(os.Stderr)
	if err := command.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, safeCommandError(err))
		os.Exit(1)
	}
}
