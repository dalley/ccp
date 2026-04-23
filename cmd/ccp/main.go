package main

import (
	"fmt"
	"os"

	"github.com/dalley/ccp/internal/cli"
)

func main() {
	if err := cli.NewRoot().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "ccp:", err)
		os.Exit(cli.ExitCodeFor(err))
	}
}
