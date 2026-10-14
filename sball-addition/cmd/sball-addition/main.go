// Command sball-addition is the managed hook system for the sballd (PVM)
// service: it runs administrator supplied scripts before the service starts
// (pre-quorate) and once the node has joined the cluster (post-quorate).
package main

import (
	"os"

	"sball-addition/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:]))
}
