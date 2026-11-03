package sandbox

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

const seatbelt = "/usr/bin/sandbox-exec"

func available() error {
	if _, err := exec.LookPath(seatbelt); err != nil {
		return fmt.Errorf("%w: %s is missing", ErrUnavailable, seatbelt)
	}
	return nil
}

// wrap runs the command under a Seatbelt profile. Later rules win, so the
// profile allows by default, then denies writes, the network and secrets,
// then allows back exactly what the command needs.
func wrap(cfg Config, argv []string) ([]string, error) {
	var p strings.Builder
	p.WriteString("(version 1)\n(allow default)\n")
	if !cfg.Network {
		p.WriteString("(deny network-outbound (remote ip))\n(allow network-outbound (remote ip \"localhost:*\"))\n")
	}
	// Writes: only the workspace (unless read-only) and scratch.
	p.WriteString("(deny file-write*)\n(allow file-write*")
	if !cfg.ReadOnly {
		p.WriteString(" (subpath " + sbQuote(cfg.Workspace) + ")")
	}
	p.WriteString(" (subpath " + sbQuote(cfg.Scratch) + ")" +
		` (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper") (regex #"^/dev/tty") (regex #"^/dev/fd/"))` + "\n")
	fmt.Fprintf(&p, "(deny file-write* (subpath %s) (subpath %s))\n",
		sbQuote(filepath.Join(cfg.Workspace, ".git")), sbQuote(filepath.Join(cfg.Workspace, ".ackn4th")))
	// Reads: no secrets, wherever they are.
	p.WriteString("(deny file-read* file-write*")
	for _, h := range hidden(cfg) {
		p.WriteString(" (subpath " + sbQuote(h) + ")")
	}
	// Names match in any case, as the filesystem does.
	for _, re := range []string{`/\.env(\.[^/]*)?$`, `\.(pem|key|p12|pfx)$`, `/id_(rsa|ed25519)[^/]*$`, `/\.(netrc|npmrc|pypirc)$`} {
		p.WriteString(` (regex #"` + anyCase(re) + `")`)
	}
	p.WriteString(")\n")
	fmt.Fprintf(&p, "(allow file-read* (require-all (subpath %s) (regex #\"%s\")))\n", sbQuote(cfg.Workspace), anyCase(`\.(example|sample|template|dist)$`))
	// Scratch may sit inside ackn4th's hidden data dir.
	fmt.Fprintf(&p, "(allow file-read* file-write* (subpath %s))\n", sbQuote(cfg.Scratch))
	// Tools that resolve paths (Node's realpath, Copilot's directory
	// checks) stat every parent: let them see that the parents exist, not
	// what's in them.
	if anc := ancestors(append([]string{cfg.Scratch, cfg.Workspace}, cfg.Keep...)); len(anc) > 0 {
		p.WriteString("(allow file-read-metadata")
		for _, a := range anc {
			p.WriteString(" (literal " + sbQuote(a) + ")")
		}
		p.WriteString(")\n")
	}
	// An agent CLI's own state.
	for _, k := range cfg.Keep {
		fmt.Fprintf(&p, "(allow file-read* file-write* (subpath %s))\n", sbQuote(k))
	}
	for _, f := range cfg.Fixed {
		fmt.Fprintf(&p, "(deny file-write* (subpath %s))\n", sbQuote(f))
	}
	// No keychain, and nothing started outside the sandbox: no apps,
	// Apple Events or Launch Services.
	keychain := `(global-name "com.apple.SecurityServer") (global-name "com.apple.securityd")`
	if cfg.Keychain {
		keychain = ""
	}
	p.WriteString(`(deny mach-lookup ` + keychain +
		` (global-name-regex #"^com\.apple\.lsd\.") (global-name-regex #"^com\.apple\.coreservices\.") (global-name-regex #"^com\.apple\.launchservices"))` + "\n")
	p.WriteString("(deny appleevent-send)\n")
	return append([]string{seatbelt, "-p", p.String()}, argv...), nil
}

// anyCase makes a regex's letters match either case ([eE] for e), since
// Seatbelt's regexes have no case-insensitive flag.
func anyCase(re string) string {
	var b strings.Builder
	for _, r := range re {
		if lo, up := strings.ToLower(string(r)), strings.ToUpper(string(r)); lo != up {
			b.WriteString("[" + lo + up + "]")
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ancestors lists the parent directories of paths, without duplicates.
func ancestors(paths []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range paths {
		for d := filepath.Dir(p); !seen[d]; d = filepath.Dir(d) {
			seen[d] = true
			out = append(out, d)
			if d == filepath.Dir(d) {
				break
			}
		}
	}
	return out
}
