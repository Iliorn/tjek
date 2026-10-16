package app

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
)

// agents.go tells which coding agent, if any, runs a command, so what it does
// carries its name with nothing set up: its claims (claimHolder) and the
// history of its edits (cliAuthorName).

// agentName is the agent running tjek, named by its kind and, when it says
// which chat it is in, four hex digits standing for the chat: claude-2b91.
// chat reports the digits are there, so the name is the same on every call
// from that chat and no other chat's. name is "" when no agent is running tjek.
func agentName() (name string, chat bool) {
	kind, session := detectAgent(os.Getenv)
	if kind == "" || session == "" {
		return kind, false
	}
	return kind + "-" + shortHash(session), true
}

// cliAuthorName is the name the CLI signs a change in the history with: a name
// given (TJEK_AUTHOR), else the agent running it (agentName), else the Settings
// name or the account's. The app signs with authorName: it is the person's.
func cliAuthorName(s appSettings) string {
	if n := strings.TrimSpace(os.Getenv("TJEK_AUTHOR")); n != "" {
		return n
	}
	if n, _ := agentName(); n != "" {
		return n
	}
	return authorName(s)
}

// detectAgent names the coding agent whose shell runs tjek, from what it puts
// in its commands' environment, and its session when it says which: kind is
// "" for none. AI_AGENT is the convention an agent with no variable known here
// may follow ("claude-code_2-1-285_agent" names "claude-code").
func detectAgent(getenv func(string) string) (kind, session string) {
	switch {
	case getenv("CLAUDECODE") != "":
		return "claude", getenv("CLAUDE_CODE_SESSION_ID")
	case getenv("CODEX_THREAD_ID") != "":
		return "codex", getenv("CODEX_THREAD_ID")
	case getenv("CODEX_SANDBOX") != "", getenv("CODEX_SANDBOX_NETWORK_DISABLED") != "", getenv("CODEX_CI") != "":
		return "codex", ""
	case getenv("AI_AGENT") != "":
		return agentKind(getenv("AI_AGENT")), ""
	}
	return "", ""
}

// agentKind is AI_AGENT's leading name, cut at its first "_" and kept to
// letters, digits and dashes, so it reads well inside a name.
func agentKind(v string) string {
	v, _, _ = strings.Cut(strings.ToLower(strings.TrimSpace(v)), "_")
	v = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' {
			return r
		}
		return -1
	}, v)
	v = strings.Trim(v, "-")
	if len(v) > 16 {
		v = strings.TrimRight(v[:16], "-")
	}
	if v == "" {
		return "agent"
	}
	return v
}

// shortHash is four hex digits standing for s: enough to tell one session from
// another, and none of the session's own ID on show.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:2])
}

func randomHex() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
