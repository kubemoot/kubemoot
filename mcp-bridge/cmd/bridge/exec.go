package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// runExec redirects stdin/stdout to named pipes and execs the given command.
// This replaces `sh -c "exec <cmd> < /pipes/stdin > /pipes/stdout"` and works
// on any image including scratch (no shell required).
func runExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	pipeDir := fs.String("pipe-dir", "/pipes", "Directory containing stdin/stdout FIFOs")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kubemoot-mcp-bridge exec [flags] -- <command> [args...]\n\n")
		fmt.Fprintf(os.Stderr, "Redirects stdin/stdout to named pipes and execs the command.\n\n")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	cmdArgs := fs.Args()
	if len(cmdArgs) == 0 {
		fmt.Fprintf(os.Stderr, "exec: no command specified after '--'\n")
		os.Exit(1)
	}

	stdinPath := filepath.Join(*pipeDir, "stdin")
	stdoutPath := filepath.Join(*pipeDir, "stdout")

	// Open stdin pipe for reading (blocks until bridge opens write end)
	stdinFile, err := os.OpenFile(stdinPath, os.O_RDONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec: open stdin pipe: %v\n", err)
		os.Exit(1)
	}

	// Open stdout pipe for writing (blocks until bridge opens read end)
	stdoutFile, err := os.OpenFile(stdoutPath, os.O_WRONLY, 0)
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec: open stdout pipe: %v\n", err)
		os.Exit(1)
	}

	// Redirect stdin/stdout to pipes
	if err := syscall.Dup2(int(stdinFile.Fd()), 0); err != nil {
		fmt.Fprintf(os.Stderr, "exec: dup2 stdin: %v\n", err)
		os.Exit(1)
	}
	if err := syscall.Dup2(int(stdoutFile.Fd()), 1); err != nil {
		fmt.Fprintf(os.Stderr, "exec: dup2 stdout: %v\n", err)
		os.Exit(1)
	}

	stdinFile.Close()
	stdoutFile.Close()

	// Resolve binary path (absolute paths pass through, relative use PATH)
	binary, err := exec.LookPath(cmdArgs[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "exec: %v\n", err)
		os.Exit(1)
	}

	// Replace this process with the command — pipe-exec disappears
	if err := syscall.Exec(binary, cmdArgs, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "exec: exec %s: %v\n", binary, err)
		os.Exit(1)
	}
}
