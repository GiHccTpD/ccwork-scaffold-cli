package main

import (
	"fmt"
	"os"

	"git.ccwork.com/ccwork/go/ccwork-scaffold-cli/internal/cli"
)

// Main 执行 ccwork-scaffold 命令行入口。
func main() {
	command := cli.NewCommand(os.Stdout, os.Stderr)
	if err := command.Execute(); err != nil {
		var exitErr *cli.ExitError
		if !cli.AsExitError(err, &exitErr) {
			exitErr = &cli.ExitError{Code: cli.ExitGeneral, Err: err}
		}
		fmt.Fprintln(os.Stderr, exitErr.Error())
		os.Exit(int(exitErr.Code))
	}
}
