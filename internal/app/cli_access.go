package app

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
)

// cli_access.go is what a command may do to the store, and how its failure
// reads to a script. Agents run tjek without a terminal and parse what it
// prints, so a lookup must not change anything, and an error must come back
// in the form the command was asked to answer in.

// cliReads reports whether a subcommand only reads the store. It opens an
// existing store and writes nothing to it (accessRead): no store is created,
// no timer is recovered, no tombstone pruned. These are also the commands a
// read-only run allows.
func cliReads(cmd string) bool {
	switch cmd {
	case "list", "ls", "top", "show", "why", "search", "tags", "projects", "stats", "export", "doctor":
		return true
	}
	return false
}

// cliStoreFree reports whether a subcommand says nothing about tasks and so
// never opens the store at all. Opening it is not free: a store older than
// this binary is migrated on open, which is a one-way change for anyone still
// running an older tjek elsewhere, and `tjek update --check` asking GitHub
// for a tag must not be what upgrades a database schema.
func cliStoreFree(cmd string) bool {
	switch cmd {
	case "help", "-h", "--help", "--version", "update", "completion", "man":
		return true
	}
	return false
}

// readOnlyFlag is the global flag that makes a run read-only; TJEK_READ_ONLY
// does the same from the environment, which is where an agent's sandbox
// configuration puts it.
const readOnlyFlag = "--read-only"

// takeReadOnly removes --read-only from args (anywhere before a "--") and
// reports whether the run is read-only, by the flag or by TJEK_READ_ONLY.
func takeReadOnly(args []string) ([]string, bool) {
	ro := envTrue(os.Getenv("TJEK_READ_ONLY"))
	out := make([]string, 0, len(args))
	for i, a := range args {
		if a == "--" {
			out = append(out, args[i:]...)
			break
		}
		if a == readOnlyFlag {
			ro = true
			continue
		}
		out = append(out, a)
	}
	return out, ro
}

func envTrue(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// refuseInReadOnly answers a command a read-only run does not allow.
func refuseInReadOnly(cmd string) int {
	fmt.Fprintf(os.Stderr, "tjek %s: refused in a read-only run (--read-only or TJEK_READ_ONLY); it may change the store. Read-only commands: list, top, show, why, search, tags, projects, stats, export, doctor\n", cmd)
	return 1
}

// wantsJSON reports whether args ask for a JSON answer: --json, or stats'
// --format=json.
func wantsJSON(args []string) bool {
	for i, a := range args {
		switch {
		case a == "--":
			return false
		case a == "--json", a == "-json", a == "--json=true", a == "-json=true":
			return true
		case a == "--format=json", a == "-format=json":
			return true
		case (a == "--format" || a == "-format") && i+1 < len(args) && args[i+1] == "json":
			return true
		}
	}
	return false
}

// cliError is a failed --json command's answer on stdout: what it said on
// stderr, and the exit code it ended with (1 runtime, 2 usage, 3 the task is
// claimed or no task was free to claim).
type cliError struct {
	Error    string `json:"error"`
	ExitCode int    `json:"exit_code"`
}

// withJSONErrors runs a command asked for JSON so that a failure answers in
// JSON too: a script parsing stdout gets an object saying what went wrong
// rather than nothing. Stderr still says it in words, as it happens (prompts
// included), and the exit code is the command's own.
func withJSONErrors(run func() int) int {
	realStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		return run()
	}
	var said bytes.Buffer
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.MultiWriter(realStderr, &said), r)
		close(done)
	}()
	os.Stderr = w
	rc := func() int {
		defer func() { os.Stderr = realStderr }()
		return run()
	}()
	w.Close()
	<-done
	r.Close()
	if rc != 0 {
		msg := strings.TrimSpace(said.String())
		if msg == "" {
			msg = fmt.Sprintf("exit status %d", rc)
		}
		emitJSON(cliError{Error: msg, ExitCode: rc})
	}
	return rc
}
