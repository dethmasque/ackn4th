package sandbox

// Helpers for the Windows backend that don't need Windows, so they're
// tested everywhere.

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
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
