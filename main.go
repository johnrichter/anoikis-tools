// Command anoikis drives a project as a graph of work nodes with dependency
// edges: one next action per call, surface-disjoint parallel admission, an
// always-on post-merge backstop, and a kill-safe resume from an append-only
// run log.
//
// The engine is harness-agnostic. Stages, roles, routes, the gate vocabulary,
// document mirrors, the resource domains a surface may claim and the
// post-merge check are all injected as a harness policy file.
package main

import (
	"os"

	"github.com/johnrichter/anoikis-tools/cmd"
)

func main() {
	os.Exit(cmd.Execute())
}
