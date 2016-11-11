/*
Copyright 2016 Palantir Technologies, Inc.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"errors"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/crypto/ssh/terminal"

	"github.com/palantir/pkg/cli/flag"
)

var (
	versionFlag        = flag.BoolFlag{Name: "version", Usage: "print version and exit"}
	helpFlag           = flag.BoolFlag{Name: "help", Alias: "h", Usage: "print help and exit"}
	ErrDisplayHelpText = errors.New("display help text")
)

func (app *App) Run(args []string) (exitStatus int) {
	addManpageCommand(app)
	app.Flags = append(app.Flags, versionFlag)
	addHelpFlags(&app.Command)

	if code := app.doCompletion(args); code != -1 {
		return code
	}

	ctx, err := app.parse(args)
	if len(ctx.Path) == 0 && ctx.Bool(versionFlag.MainName()) {
		printVersion(ctx)
		return 0
	}
	if ctx.Bool(helpFlag.MainName()) {
		ctx.PrintHelp(app.Stdout)
		return 0
	}
	if err != nil {
		ctx.Errorf("%v\n\n", err)
		ctx.PrintHelp(app.Stderr)
		return 1
	}

	if ctx.Command.Action == nil {
		ctx.PrintHelp(app.Stderr)
		return 1
	}

	if app.Before != nil {
		if err := app.Before(ctx); err != nil {
			return app.handleError(ctx, err)
		}
	}

	printDeprecationNotices(ctx)
	if err := runAction(ctx); err == ErrDisplayHelpText {
		ctx.PrintHelp(app.Stderr)
		return 1
	} else if err != nil {
		return app.handleError(ctx, err)
	}
	return 0
}

func (app *App) handleError(ctx Context, err error) int {
	if app.ErrorHandler != nil {
		return app.ErrorHandler(ctx, err)
	}

	if err.Error() != "" {
		ctx.Errorln(err)
	}
	if exitCoder, ok := err.(ExitCoder); ok {
		return exitCoder.ExitCode()
	}
	return 1
}

func addHelpFlags(cmd *Command) {
	cmd.Flags = append(cmd.Flags, helpFlag)
	for i := range cmd.Subcommands {
		addHelpFlags(&cmd.Subcommands[i])
	}
}

func printVersion(ctx Context) {
	version := ctx.App.Version
	if version == "" {
		version = "unknown"
	}
	ctx.Printf("%v version %v\n", ctx.App.Name, version)
}

func printDeprecationNotices(ctx Context) {
	for _, f := range ctx.Command.Flags {
		if ctx.Has(f.MainName()) && f.DeprecationStr() != "" {
			ctx.Errorln(f.DeprecationStr())
		}
	}
}

func runAction(ctx Context) error {
	// In case Action changes termios on stdin without resetting them.
	// This can happen if terminal.ReadPassword is interrupted with SIGINT.
	// If tcgetattr fails to get the initial state of stdin, just ignore.
	stdin := int(os.Stdin.Fd())
	// capture initial state; ignore error
	if initialState, err := terminal.GetState(stdin); err == nil {
		ctx.App.OnExit.register(func() {
			// restore terminal (ignore error)
			_ = terminal.Restore(stdin, initialState)
		},
			highPriority)
	}

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)

	// use 'once' to ensure that onExit handlers are only called once. Guards against the case where an
	// interrupt signal is received during the onExit call in the defer -- in such a case, if the defer call
	// is already executing the onExit handlers, the signal handler should not invoke the handlers again.
	var (
		once   sync.Once
		onExit = func() {
			// run all OnExit functions
			ctx.App.OnExit.run()
		}
	)

	// runs if cli exits because of a signal
	go func() {
		if _, ok := <-signals; ok {
			once.Do(onExit)
			os.Exit(1)
		}
	}()
	// runs if cli exits normally
	defer func() {
		signal.Stop(signals)
		close(signals)
		once.Do(onExit)
	}()

	return ctx.Command.Action(ctx)
}
