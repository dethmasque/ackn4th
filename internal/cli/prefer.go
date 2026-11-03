package cli

import (
	"context"
	"sync"
	"time"

	"github.com/dethmasque/ackn4th/internal/auth"
	"github.com/dethmasque/ackn4th/internal/provider/claudecode"
	"github.com/dethmasque/ackn4th/internal/provider/clis"
	"github.com/dethmasque/ackn4th/internal/sandbox"
)

// cliProviders maps an API provider to the provider that drives its local
// CLI instead. Gemini's CLI has been retired for most users (replaced by
// Antigravity), so Gemini stays on its API.
var cliProviders = map[string]string{
	"claude": claudecode.Name,
	"openai": clis.CodexName,
}

var (
	cliReadyMu sync.Mutex
	cliReady   = map[string]bool{}
)

// cliSignedIn reports, once per run, whether a CLI provider is installed
// and signed in.
func cliSignedIn(p string) bool {
	cliReadyMu.Lock()
	defer cliReadyMu.Unlock()
	if ok, seen := cliReady[p]; seen {
		return ok
	}
	ok := false
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch {
	case p == claudecode.Name:
		_, err := claudecode.Status(ctx, "")
		ok = err == nil
	case auth.CLIDriver(p) != nil:
		_, err := auth.CLIDriver(p).Status(ctx)
		ok = err == nil && sandbox.Available() == nil && sandbox.Loopback() == nil
	}
	cliReady[p] = ok
	return ok
}

// preferCLI is the provider to use when p was picked by default rather
// than named: its CLI provider, when prefer_cli is on and the CLI is
// ready.
func (a *app) preferCLI(p string) string {
	if !a.cfg.PreferCLI {
		return p
	}
	if cli, ok := cliProviders[p]; ok && cliSignedIn(cli) {
		return cli
	}
	return p
}
