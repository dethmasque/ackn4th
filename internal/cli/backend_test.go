package cli

// Tests for the choice the README presents: the local data dir by default, the
// controller named by ACKN4TH_BACKEND_URL when there is one. Which one is live
// decides whether a command acts on this machine or asks another one to.

import (
	"strings"
	"testing"

	"github.com/dethmasque/ackn4th/internal/config"
	"github.com/dethmasque/ackn4th/internal/task"
)

// loadApp builds the app the way the binary does, so these tests cover the
// real selection path rather than a hand-wired app.
func loadApp(t *testing.T, backendURL string) *app {
	t.Helper()
	t.Setenv(config.HomeEnv, t.TempDir())
	t.Setenv(task.EnvBackendURL, backendURL)
	a := &app{noInput: true}
	if err := a.load(); err != nil {
		t.Fatalf("load (ACKN4TH_BACKEND_URL=%q): %v", backendURL, err)
	}
	t.Cleanup(a.closeComm)
	return a
}

// With no ACKN4TH_BACKEND_URL the file backend is live and commands act here:
// reading a store as "remote" would send every local start and stop to a
// controller that cannot exist.
func TestDefaultBackendIsLocal(t *testing.T) {
	a := loadApp(t, "")
	if a.remote() {
		t.Fatal("remote() is true with no ACKN4TH_BACKEND_URL: local start/stop would be routed to a controller")
	}
	if !a.localRunner() {
		t.Fatal("localRunner() is false with no ACKN4TH_BACKEND_URL: nothing would run a task on this machine")
	}
	if _, ok := a.backend().(*task.Store); !ok {
		t.Fatalf("default backend = %T, want the file store", a.backend())
	}
	if a.controller() != nil {
		t.Fatal("the file backend offers a control plane")
	}
}

// A controller URL makes the state shared, so the runner belongs to the
// daemon host and this process asks it. Nothing here needs a controller to be
// listening: the point is which backend the environment chose.
func TestControllerURLMakesTheBackendRemote(t *testing.T) {
	a := loadApp(t, "ackn://127.0.0.1:7602")
	if !a.remote() {
		t.Fatal("remote() is false with a controller URL: the client would run the task itself")
	}
	if a.localRunner() {
		t.Fatal("localRunner() is true against a controller: two runners could claim one task")
	}
	if _, ok := a.backend().(*task.GRPC); !ok {
		t.Fatalf("backend with a controller URL = %T, want *task.GRPC", a.backend())
	}
	if a.controller() == nil {
		t.Fatal("a controller URL offers no control plane: start and stop would go nowhere")
	}
	ep, ok := a.backend().(task.Endpointer)
	if !ok {
		t.Fatal("the controller backend cannot say where it points, so status cannot show it")
	}
	if got := ep.Endpoint(); !strings.Contains(got, "127.0.0.1:7602") {
		t.Errorf("endpoint = %q, want the controller's address", got)
	}
}

// A URL this build cannot speak is refused by name at load, not silently
// treated as a host name.
func TestAnUnsupportedBackendURLIsRefused(t *testing.T) {
	t.Setenv(config.HomeEnv, t.TempDir())
	t.Setenv(task.EnvBackendURL, "http://127.0.0.1:7602")
	a := &app{noInput: true}
	err := a.load()
	if err == nil {
		t.Fatal("load accepted http:// as a backend URL")
	}
	if !strings.Contains(err.Error(), task.EnvBackendURL) {
		t.Errorf("the refusal does not name the variable: %v", err)
	}
}
