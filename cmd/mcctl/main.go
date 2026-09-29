// Command mcctl is the CLI client for mcctld.
package main

import (
	"os"

	"github.com/takiren/mcupdown/cmd/mcctl/cmd"
)

func main() {
	os.Exit(cmd.Run(os.Args[1:], os.Stdout, os.Stderr))
}
