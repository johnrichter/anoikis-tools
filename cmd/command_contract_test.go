package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestCommandContract locks the two guarantees the help text depends on:
// every command carries a real Long description, and every command name is
// demonstrated somewhere in the package's pooled Example fields.
func TestCommandContract(t *testing.T) {
	root := newRootCmd()

	pool := map[string]bool{}
	walk(root, func(c *cobra.Command) {
		for _, word := range strings.Fields(c.Example) {
			pool[word] = true
		}
	})

	walk(root, func(c *cobra.Command) {
		if strings.TrimSpace(c.Long) == "" {
			t.Errorf("command %q declares no Long description", c.CommandPath())
		}
		name := strings.Fields(c.Use)[0]
		if c == root {
			return
		}
		if !pool[name] {
			t.Errorf("command %q: name %q appears in no Example field in the cmd package", c.CommandPath(), name)
		}
	})
}

// walk visits a command and every descendant.
func walk(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walk(sub, fn)
	}
}
