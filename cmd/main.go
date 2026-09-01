package main

import (
	"fmt"
	"os"

	"git.ccwork.com/ccwork/go/ccwork-scaffold-cli/internal"
)

// main 执行 ccwork-scaffold 命令行入口。
func main() {
	command := internal.NewCommand(os.Stdout, os.Stderr)
	if err := command.Execute(); err != nil {
		var exitErr *internal.ExitError
		if !internal.AsExitError(err, &exitErr) {
			exitErr = &internal.ExitError{Code: internal.ExitGeneral, Err: err}
		}
		fmt.Fprintln(os.Stderr, exitErr.Error())
		os.Exit(int(exitErr.Code))
	}
}
