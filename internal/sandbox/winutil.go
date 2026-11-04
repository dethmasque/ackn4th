package sandbox

// Helpers for the Windows backend that don't need Windows, so they're
// tested everywhere.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// ace is one access control entry from an SDDL string.
type ace struct {
	Type      string // "A" allow, "D" deny, …
	Flags     string // "OICI", "OICIID", …
	Rights    string
	SID       string
	Inherited bool
}

var aceRE = regexp.MustCompile(`\(([A-Z]+);([A-Z]*);([^;]*);[^;]*;[^;]*;([^;)]+)\)`)

// parseACEs returns the entries of the DACL in an SDDL string.
func parseACEs(sddl string) []ace {
	if i := strings.Index(sddl, "D:"); i >= 0 {
		sddl = sddl[i+2:]
		if j := strings.Index(sddl, "S:"); j >= 0 {
			sddl = sddl[:j]
		}
	}
	var out []ace
	for _, m := range aceRE.FindAllStringSubmatch(sddl, -1) {
		out = append(out, ace{Type: m[1], Flags: m[2], Rights: m[3], SID: m[4], Inherited: strings.Contains(m[2], "ID")})
	}
	return out
}

// hasExplicit reports whether the DACL has an entry of the type ("A" or
// "D") for sid set on the object itself, not inherited.
func hasExplicit(sddl, typ, sid string) bool {
	for _, a := range parseACEs(sddl) {
		if a.Type == typ && !a.Inherited && strings.EqualFold(a.SID, sid) {
			return true
		}
	}
	return false
}

var labelRE = regexp.MustCompile(`\(ML;[A-Z]*;[A-Z]*;;;LW\)`)

// hasLowLabel reports whether an SDDL string's SACL labels the object low
// integrity, which AppContainer processes need to write it.
func hasLowLabel(sddl string) bool { return labelRE.MatchString(sddl) }

var npmShimRE = regexp.MustCompile(`"%dp0%\\([^"]+\.(?:js|cjs|mjs))"`)

// npmShimScript returns the script an npm .cmd shim runs with node,
// relative to the shim's directory, or "" if content isn't an npm shim.
func npmShimScript(content string) string {
	m := npmShimRE.FindStringSubmatch(content)
	if m == nil {
		return ""
	}
	return m[1]
}

// capName names an AppContainer capability for one path and use, so the
// grant on the path's ACL matches only processes given that capability.
func capName(kind, path string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(path)))
	return "ackn4th." + kind + "." + hex.EncodeToString(sum[:8])
}

// appSID reports whether an SDDL trustee is an AppContainer identity: a
// package (S-1-15-2-…, AC for ALL APPLICATION PACKAGES) or a capability
// (S-1-15-3-…).
func appSID(sid string) bool {
	s := strings.ToUpper(sid)
	return s == "AC" || strings.HasPrefix(s, "S-1-15-2-") || strings.HasPrefix(s, "S-1-15-3-")
}

// readExecute is FILE_GENERIC_READ | FILE_GENERIC_EXECUTE.
const readExecute = "0x1200a9"

// splitDACL returns the DACL's flags ("PAI", …) and its entries, each in
// parentheses, from an SDDL string. Entries with nested parentheses
// (conditional entries, resource attributes) are refused: restrictDACL
// would have to understand them to copy them faithfully.
func splitDACL(sddl string) (flags string, entries []string, err error) {
	i := strings.Index(sddl, "D:")
	if i < 0 {
		return "", nil, nil // no DACL at all: everyone has access
	}
	d := sddl[i+2:]
	if j := strings.Index(d, "S:"); j >= 0 {
		d = d[:j]
	}
	k := strings.IndexByte(d, '(')
	if k < 0 {
		return d, nil, nil
	}
	flags, d = d[:k], d[k:]
	for d != "" {
		if d[0] != '(' {
			return "", nil, fmt.Errorf("unexpected %q in the DACL", d)
		}
		end := strings.IndexByte(d, ')')
		if end < 0 || strings.Contains(d[1:end], "(") {
			return "", nil, fmt.Errorf("an entry the sandbox can't copy: %q", d)
		}
		entries = append(entries, d[:end+1])
		d = d[end+1:]
	}
	return flags, entries, nil
}

// restrictDACL rewrites a path's DACL, as SDDL, so that AppContainers
// reach it only through the allow entries given: read and execute for
// each SID in allow, nothing for any other package or capability. Every
// other entry is kept, inherited ones made explicit, and the DACL is
// protected, so an inheritable grant on a parent (the workspace's) no
// longer flows in.
//
// This is how the Windows sandbox keeps .git read-only and credential
// files unreadable inside a workspace it grants: the AppContainer access
// check ignores deny entries for packages and capabilities, so access has
// to be left out, not denied.
//
// It returns the new DACL and whether it differs from the old one, so a
// path already restricted isn't rewritten on every launch.
func restrictDACL(sddl string, allow []string, dir bool) (string, bool, error) {
	flags, entries, err := splitDACL(sddl)
	if err != nil {
		return "", false, err
	}
	changed := !strings.Contains(flags, "P")
	var out []string
	have := map[string]bool{}
	for _, e := range entries {
		f := strings.Split(e[1:len(e)-1], ";")
		if len(f) < 6 {
			return "", false, fmt.Errorf("an entry the sandbox can't read: %q", e)
		}
		if appSID(f[5]) {
			// Ours, made by an earlier launch: keep it if it is exactly
			// what's wanted.
			want := slices.ContainsFunc(allow, func(s string) bool { return strings.EqualFold(s, f[5]) })
			if want && f[0] == "A" && !strings.Contains(f[1], "ID") && strings.EqualFold(f[2], readExecute) && !have[strings.ToUpper(f[5])] {
				have[strings.ToUpper(f[5])] = true
				out = append(out, e)
				continue
			}
			changed = true
			continue
		}
		if strings.Contains(f[1], "ID") {
			f[1] = strings.Replace(f[1], "ID", "", 1)
			e = "(" + strings.Join(f, ";") + ")"
			changed = true
		}
		out = append(out, e)
	}
	inherit := ""
	if dir {
		inherit = "OICI"
	}
	for _, s := range allow {
		if !have[strings.ToUpper(s)] {
			out = append(out, "(A;"+inherit+";"+readExecute+";;;"+s+")")
			changed = true
		}
	}
	return "D:P" + strings.ReplaceAll(strings.ReplaceAll(flags, "P", ""), "D:", "") + strings.Join(out, ""), changed, nil
}
