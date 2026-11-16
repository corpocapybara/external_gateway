// Command leakcheck refuses commits that add the names of real workspaces, projects or internal hosts
// (EG0047). It runs from the git hooks pre-commit.local and commit-msg.local that `leakcheck install`
// writes; see README.md and docs/design/EG0047-leakcheck-hook.md.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() {
	os.Exit(run(os.Args[1:], ".", os.Stdout, os.Stderr))
}

// run dispatches a subcommand in dir and returns the process exit code: 0 clean, 1 refused or error,
// 2 usage error.
func run(args []string, dir string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return usage(stderr)
	}
	switch args[0] {
	case "pre-commit":
		return runPreCommit(dir, stderr)
	case "commit-msg":
		if len(args) != 2 {
			return usage(stderr)
		}
		return runCommitMsg(dir, args[1], stderr)
	case "install":
		return runInstall(dir, stdout, stderr)
	}
	return usage(stderr)
}

func usage(stderr io.Writer) int {
	fmt.Fprintln(stderr, "usage: leakcheck pre-commit | commit-msg FILE | install")
	return 2
}
