package app

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The --json modes are tjek's only machine-readable surface, and the only
// part of it somebody else's script depends on. A rename in a struct tag, a
// field dropped during a refactor, an array that quietly becomes an object —
// none of that fails a build, none of it fails an existing test that checks
// one field it happens to care about, and all of it breaks every script
// downstream at once. Nothing in the tree pinned the *shape*.
//
// These tests pin exactly that: the set of key paths each command emits. Not
// the values — ids, timestamps and scores are supposed to vary — so the golden
// files stay readable and only move when the contract genuinely moves. When
// one fails it is asking a question, not reporting a bug: is this field really
// leaving, and is that a breaking change for anyone parsing it?
//
// Regenerate after an intentional change with:
//
//	go test -run TestJSONContract -update-json-contract ./...
//
// and read the resulting diff as the changelog entry it probably needs.

var updateJSONContract = flag.Bool("update-json-contract", false, "rewrite the --json contract golden files")

// jsonKeyPaths flattens a decoded JSON document into sorted, de-duplicated key
// paths. An array contributes "[]" once, however many elements it has, so a
// fixture with two tasks and a fixture with three produce the same contract —
// what is pinned is the shape, not the data.
func jsonKeyPaths(v any, prefix string, out map[string]bool) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 0 {
			out[prefix+"{}"] = true
			return
		}
		for k, sub := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			jsonKeyPaths(sub, p, out)
		}
	case []any:
		if len(t) == 0 {
			// An empty array still proves the field is an array. Record it as
			// such rather than losing the path entirely, which would make the
			// contract depend on the fixture having data for every field.
			out[prefix+"[]"] = true
			return
		}
		for _, sub := range t {
			jsonKeyPaths(sub, prefix+"[]", out)
		}
	default:
		if prefix == "" {
			prefix = "(scalar)"
		}
		out[prefix] = true
	}
}

// contractOf runs one CLI invocation, decodes whatever JSON it printed, and
// returns the sorted key paths.
func contractOf(t *testing.T, args ...string) string {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = dispatchCLI(args) })
	if code != 0 {
		t.Fatalf("%v: exit %d\noutput: %s", args, code, out)
	}
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v: output is not valid JSON: %v\noutput: %s", args, err, out)
	}
	paths := map[string]bool{}
	jsonKeyPaths(doc, "", paths)
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, "\n") + "\n"
}

func checkContract(t *testing.T, name string, got string) {
	t.Helper()
	golden := filepath.Join("testdata", "json_contract", name+".txt")
	if *updateJSONContract {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	raw, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update-json-contract to create it)", golden, err)
	}
	// The golden holds one key path per line, and a line ending is not part of
	// the contract it records. A Windows checkout hands these files back with
	// CRLF, which made every key differ from its own self — the whole file
	// reported as removed and re-added, on the one platform where nothing had
	// actually changed. .gitattributes pins the checkout too; this is the half
	// that does not depend on the clone being configured correctly.
	want := strings.ReplaceAll(string(raw), "\r\n", "\n")
	if got != want {
		t.Errorf("`tjek %s --json` shape changed.\n%s\nRerun with -update-json-contract if this is intended — and consider whether it breaks somebody's script.",
			name, diffLines(want, got))
	}
}

// diffLines reports which key paths appeared and disappeared, which is the
// only part of a contract change anyone needs to read.
func diffLines(want, got string) string {
	in := func(s string) map[string]bool {
		m := map[string]bool{}
		for _, l := range strings.Split(strings.TrimSpace(s), "\n") {
			m[l] = true
		}
		return m
	}
	w, g := in(want), in(got)
	var b strings.Builder
	for _, l := range strings.Split(strings.TrimSpace(want), "\n") {
		if !g[l] {
			fmt.Fprintf(&b, "  removed: %s\n", l)
		}
	}
	for _, l := range strings.Split(strings.TrimSpace(got), "\n") {
		if !w[l] {
			fmt.Fprintf(&b, "  added:   %s\n", l)
		}
	}
	return b.String()
}

// jsonContractFixture builds a store carrying as many of the optional fields as
// one fixture can: every field on todo.Todo below is `omitempty`, so a task
// with no due date and no tags would pin a contract with half its shape
// missing and the golden files would say nothing about the fields most
// scripts actually read. Note `--size s` rather than `m` — SizeMedium is the
// zero value, so a medium task omits `size` entirely.
func jsonContractFixture(t *testing.T) {
	t.Helper()
	setTestHome(t, t.TempDir())
	if code := cliAdd([]string{"Contract fixture root", "--tag", "work", "--project", "atlas",
		"--priority", "high", "--due", "+30d", "--size", "s", "--note", "a note",
		"--comment", "a comment", "--recur", "weekly", "--start", "today"}); code != 0 {
		t.Fatalf("fixture add: exit %d", code)
	}
	// A completed task, so completed_at and the done-rank field are in the
	// shape too — export and `list --all` render them.
	if code := cliAdd([]string{"Contract fixture finished"}); code != 0 {
		t.Fatalf("fixture add finished: exit %d", code)
	}
	if code := cliDone([]string{"contract fixture finished"}); code != 0 {
		t.Fatalf("fixture done: exit %d", code)
	}
	if code := cliAdd([]string{"Contract fixture blocker", "--tag", "ops", "--project", "atlas"}); code != 0 {
		t.Fatalf("fixture add blocker: exit %d", code)
	}
	if code := cliSubtask([]string{"contract fixture root", "A subtask"}); code != 0 {
		t.Fatalf("fixture subtask: exit %d", code)
	}
	if code := cliEdit([]string{"contract fixture root", "--add-dep", "contract fixture blocker"}); code != 0 {
		t.Fatalf("fixture dep: exit %d", code)
	}
	if code := cliLog([]string{"contract fixture root", "30m"}); code != 0 {
		t.Fatalf("fixture log: exit %d", code)
	}
	// A claim, so the tasks that show one carry its shape.
	if code := cliClaim([]string{"contract fixture root", "--as", "contract-agent"}); code != 0 {
		t.Fatalf("fixture claim: exit %d", code)
	}
}

func TestJSONContractList(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "list", contractOf(t, "list", "--json"))
}

func TestJSONContractShow(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "show", contractOf(t, "show", "contract fixture root", "--json"))
}

func TestJSONContractSearch(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "search", contractOf(t, "search", "contract", "--json"))
}

func TestJSONContractTop(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "top", contractOf(t, "top", "--json"))
}

func TestJSONContractWhy(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "why", contractOf(t, "why", "contract fixture root", "--json"))
}

func TestJSONContractTags(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "tags", contractOf(t, "tags", "--json"))
}

func TestJSONContractProjects(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "projects", contractOf(t, "projects", "--json"))
}

func TestJSONContractStats(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "stats", contractOf(t, "stats", "--format", "json", "--seq"))
}

// The waybar shape is a third-party contract, not tjek's own: a waybar
// custom module reads exactly text/tooltip/class and nothing else.
func TestJSONContractWaybar(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "waybar", contractOf(t, "stats", "--format", "waybar"))
}

func TestJSONContractExport(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "export", contractOf(t, "export", "--include-done"))
}

func TestJSONContractDoctor(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "doctor", contractOf(t, "doctor", "--json"))
}

// claim --json is the task as show --json gives it, with the claim just made.
func TestJSONContractClaim(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "claim", contractOf(t, "claim", "--next", "--as", "another-agent", "--json"))
}

func TestJSONContractRelease(t *testing.T) {
	jsonContractFixture(t)
	checkContract(t, "release", contractOf(t, "release", "contract fixture root", "--as", "contract-agent", "--json"))
}

// A failed --json command answers in JSON too. Every command shares the one
// shape (withJSONErrors), so one failure pins it.
func TestJSONContractError(t *testing.T) {
	jsonContractFixture(t)
	var code int
	out := captureStdout(t, func() {
		captureStderr(t, func() { code = runCLI([]string{"show", "no-such-task-anywhere", "--json"}, false) })
	})
	if code != 2 {
		t.Fatalf("exit %d, want 2 (an unknown ref)\noutput: %s", code, out)
	}
	var doc any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("a failed --json command printed no JSON: %v\noutput: %s", err, out)
	}
	paths := map[string]bool{}
	jsonKeyPaths(doc, "", paths)
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	checkContract(t, "error", strings.Join(keys, "\n")+"\n")
}

// Every command the completion table says takes --json must be covered above.
// Adding a JSON surface without pinning its shape is the gap this closes, so
// the list of covered commands is derived from the table rather than kept by
// hand next to it.
func TestJSONContractCoversEveryJSONCommand(t *testing.T) {
	covered := map[string]bool{
		"list": true, "show": true, "search": true, "top": true, "why": true,
		"tags": true, "projects": true, "stats": true, "export": true, "doctor": true,
		"claim": true, "release": true,
		// `add --json` emits the created task; its shape is the same struct
		// `show --json` pins, and adding a task is not something this suite
		// should do to assert a shape it already covers.
		"add": true,
	}
	for _, spec := range cliCommandSpecs {
		for _, f := range spec.flags {
			if f != "json" {
				continue
			}
			if !covered[spec.name] {
				t.Errorf("`tjek %s --json` has no contract test — add one in cli_json_contract_test.go", spec.name)
			}
		}
	}
}
