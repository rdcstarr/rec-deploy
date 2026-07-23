// Command rec-deploy deploys GitHub repositories in place on a Linux server and
// administers their GitHub side — one static binary, CLI and webhook daemon.
package main

import (
	"github.com/rdcstarr/rec-deploy/cmd"
	"github.com/rdcstarr/rec-deploy/internal/cli"
)

// main wires a signal-aware context into the command tree and renders any
// error at the process boundary.
func main() {
	ctx, stop := cli.Context()
	defer stop()

	cli.Exit(cmd.Execute(ctx))
}
