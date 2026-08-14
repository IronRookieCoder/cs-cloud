package main

import (
	"fmt"
	"os"

	"cs-cloud/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		if !cli.CommandErrorReported(err) {
			fmt.Fprintln(os.Stderr, err)
		}
		os.Exit(cli.CommandExitCode(err))
	}
}
