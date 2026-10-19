// Package paths decides where tjek keeps its files. There are four kinds and
// they belong in four places, because that is what the platform conventions
// say:
//
//	config  settings.json, sync.json          — yours to edit, worth backing up
//	data    tasks.db (+ WAL sidecars)         — the tasks themselves
//	state   undo.json, sync-state.json, logs  — recreatable, machine-local
//	cache   the $EDITOR scratch directory     — throwaway
//
// The resolution order is the same for every kind:
//
//  1. TJEK_HOME, if set — one directory for everything, for people who would
//     rather have a single thing to back up than a tidy split.
//  2. A folder chosen in Settings, named in LocationFile — one directory for
//     everything too, for a person who would rather click than set a
//     variable, and an agent that may write only inside one folder.
//  3. An existing ~/.tjek — older installs put everything there, and moving a user's database out from under them to satisfy a
//     specification is not an upgrade. It keeps working, forever.
//  4. The platform convention: XDG on Linux/BSD (and anywhere the XDG_*
//     variables are set deliberately), %APPDATA%/%LOCALAPPDATA% on Windows,
//     ~/Library/Application Support on macOS.
//
// Rule 4 only ever applies to a fresh install, which is why rule 3 has no
// migration step and no prompt: nothing moves, nothing needs to be told.
package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// AppDirName is the directory tjek adds under each platform base.
const AppDirName = "tjek"

// LegacyDirName is the single directory every version before the XDG split
// used. Its presence is what pins an existing install to it.
const LegacyDirName = ".tjek"

// Kind is which of the four directories a file belongs in.
type Kind int

const (
	Config Kind = iota
	Data
	State
	Cache
)

// HomeOverride returns TJEK_HOME, expanded, or "" when unset. It collapses
// all four kinds into one directory.
func HomeOverride() string {
	return strings.TrimSpace(os.Getenv("TJEK_HOME"))
}

// LocationFile is the file naming a folder chosen in Settings. It lives in
// the platform's config directory (LocationPath) and holds the folder's path.
const LocationFile = "location"

// LocationPath is where the chosen folder is named: always the platform's
// own config directory, never the chosen folder or ~/.tjek, so it is found
// before anything else is resolved and survives moving out of either.
func LocationPath() string {
	base, err := platformBase(Config)
	if err != nil {
		return ""
	}
	return filepath.Join(base, AppDirName, LocationFile)
}

// Chosen is the folder LocationFile names, or "" when none is chosen. A
// relative path is no choice: it would name a different folder from every
// working directory.
func Chosen() string {
	data, err := os.ReadFile(LocationPath())
	if err != nil {
		return ""
	}
	dir := strings.TrimSpace(string(data))
	if !filepath.IsAbs(dir) {
		return ""
	}
	return dir
}

// SetChosen names dir as the folder for everything, or with "" forgets the
// choice, so the standard places apply again.
func SetChosen(dir string) error {
	path := LocationPath()
	if path == "" {
		return os.ErrNotExist
	}
	if dir == "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(dir+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// legacyHome returns ~/.tjek when it already exists as a directory.
func legacyHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	dir := filepath.Join(home, LegacyDirName)
	if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
		return dir
	}
	return ""
}

// Dir resolves one of the four directories. It does not create anything —
// callers that write go through Ensure.
func Dir(kind Kind) (string, error) {
	if override := HomeOverride(); override != "" {
		return override, nil
	}
	if chosen := Chosen(); chosen != "" {
		return chosen, nil
	}
	return Standard(kind)
}

// Standard is where a kind lives when no folder is chosen and TJEK_HOME is
// unset: ~/.tjek when it exists, else the platform convention.
func Standard(kind Kind) (string, error) {
	if legacy := legacyHome(); legacy != "" {
		return legacy, nil
	}
	base, err := platformBase(kind)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, AppDirName), nil
}

// platformBase is the parent directory for a kind, before the "tjek" element.
// An explicitly set XDG variable wins on every platform: someone who exports
// XDG_DATA_HOME on macOS or Windows means it.
func platformBase(kind Kind) (string, error) {
	if dir := xdgFromEnv(kind); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch runtime.GOOS {
	case "windows":
		// Roaming for config (it follows the user between machines), Local for
		// everything else (a database and a log do not belong in a roaming
		// profile).
		if kind == Config {
			if v := os.Getenv("APPDATA"); v != "" {
				return v, nil
			}
			return filepath.Join(home, "AppData", "Roaming"), nil
		}
		if v := os.Getenv("LOCALAPPDATA"); v != "" {
			return v, nil
		}
		return filepath.Join(home, "AppData", "Local"), nil
	case "darwin":
		// macOS has no state directory of its own; Application Support is
		// where both config and data live, and Caches is separate.
		if kind == Cache {
			return filepath.Join(home, "Library", "Caches"), nil
		}
		return filepath.Join(home, "Library", "Application Support"), nil
	default:
		return filepath.Join(append([]string{home}, xdgDefault(kind)...)...), nil
	}
}

func xdgFromEnv(kind Kind) string {
	var name string
	switch kind {
	case Config:
		name = "XDG_CONFIG_HOME"
	case Data:
		name = "XDG_DATA_HOME"
	case State:
		name = "XDG_STATE_HOME"
	case Cache:
		name = "XDG_CACHE_HOME"
	}
	v := strings.TrimSpace(os.Getenv(name))
	// The spec says a relative value is invalid and must be ignored, which
	// also keeps a stray "." from scattering files across the working
	// directory.
	if v == "" || !filepath.IsAbs(v) {
		return ""
	}
	return v
}

// xdgDefault is the spec's fallback for each kind, relative to $HOME.
func xdgDefault(kind Kind) []string {
	switch kind {
	case Config:
		return []string{".config"}
	case Data:
		return []string{".local", "share"}
	case State:
		return []string{".local", "state"}
	default:
		return []string{".cache"}
	}
}

// Ensure resolves a kind and creates it, so writers can just ask for a path.
func Ensure(kind Kind) (string, error) {
	dir, err := Dir(kind)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// For is the read-side helper: the full path of a file, whether or not its
// directory exists yet. An unresolvable home yields "", which every caller
// already treats as "no such file".
func For(kind Kind, name ...string) string {
	dir, err := Dir(kind)
	if err != nil {
		return ""
	}
	return filepath.Join(append([]string{dir}, name...)...)
}

// UsingLegacyLayout reports whether tjek is reading and writing the old single
// ~/.tjek directory. The doctor says so, since it explains why the XDG paths
// are not in use.
func UsingLegacyLayout() bool {
	return HomeOverride() == "" && Chosen() == "" && legacyHome() != ""
}
