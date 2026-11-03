package sandbox

// Windows: commands run in an AppContainer, which can read the system
// (Windows, Program Files) and nothing of the user's unless an ACL says
// so. ackn4th grants access per path with capabilities it derives for
// each one: the workspace (one capability for read-write, one for
// read-only), each scratch directory, each Keep directory, and the tool
// directories on PATH. It adds an entry for that capability to the path's
// ACL, once, and starts each process with only the capabilities it
// should have. So a command reaches its own workspace and scratch, never
// another task's, and never the user's other files.
//
// Within a granted tree, deny entries keep .git and .ackn4th read-only and
// credential files unreadable. AppContainer processes run at low
// integrity, so the paths they write are labeled low integrity too.
//
// The grants stay on the paths' ACLs. They name capabilities only ackn4th
// gives out, so other programs gain nothing from them.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// containerName is the AppContainer profile every sandboxed process runs
// in; what each may reach is down to its capabilities.
const containerName = "ackn4th.sandbox"

// launcherArg makes the ackn4th binary (or a test binary) the launcher:
// it starts the confined process inside a job object, so stopping the
// launcher stops everything the process started, and exits with its code.
const launcherArg = "__ackn4th-sandbox"

func init() {
	if len(os.Args) > 2 && os.Args[1] == launcherArg {
		// The spec on the command line is trusted to name capabilities.
		// A confined process must not be able to use this entry point to
		// start something with more of them.
		if inAppContainer() {
			fmt.Fprintln(os.Stderr, "ackn4th sandbox: refusing to launch from inside the sandbox")
			os.Exit(125)
		}
		os.Exit(launch(os.Args[2], os.Args[3:]))
	}
}

// inAppContainer reports whether this process runs in an AppContainer.
// It fails closed: if the token can't be read, it says yes.
func inAppContainer() bool {
	var v, n uint32
	const tokenIsAppContainer = 29
	err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), tokenIsAppContainer, (*byte)(unsafe.Pointer(&v)), 4, &n)
	return err != nil || v != 0
}

var (
	userenv                       = windows.NewLazySystemDLL("userenv.dll")
	procCreateAppContainerProfile = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSid     = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
	kernelbase                    = windows.NewLazySystemDLL("kernelbase.dll")
	procDeriveCapabilitySids      = kernelbase.NewProc("DeriveCapabilitySidsFromName")
	firewallapi                   = windows.NewLazySystemDLL("FirewallAPI.dll")
	procNetIsoGetConfig           = firewallapi.NewProc("NetworkIsolationGetAppContainerConfig")
)

// File access rights.
const (
	fileRead    = 0x120089 // FILE_GENERIC_READ
	fileWrite   = 0x120116 // FILE_GENERIC_WRITE
	fileExecute = 0x1200A0 // FILE_GENERIC_EXECUTE
	// DELETE only. Grants must NOT include FILE_DELETE_CHILD (0x40): with
	// it a command could rename or delete .git/.ackn4th (a deny entry on
	// the child doesn't override the parent's delete-child right) and
	// plant its own. With DELETE alone, removing an entry needs DELETE on
	// that entry, which the deny entries take away.
	fileDelete = 0x10000
	// What deny entries take away: writing (data, attributes, deleting
	// including via the parent), changing the ACL or owner, and reading
	// (data, listing, extended attributes).
	denyWrite = 0x2 | 0x4 | 0x10 | 0x100 | 0x40 | 0x40000 | 0x80000 | fileDelete
	denyRead  = 0x1 | 0x8

	procThreadAttributeSecurityCapabilities = 0x00020009
)

// Well-known network capabilities: the internet, as client and server,
// and the private network.
var networkCaps = []string{"S-1-15-3-1", "S-1-15-3-2", "S-1-15-3-3"}

var (
	probeOnce sync.Once
	probeErr  error
)

func available() error {
	probeOnce.Do(func() {
		if err := procDeriveCapabilitySids.Find(); err != nil {
			probeErr = fmt.Errorf("%w: AppContainer capabilities need Windows 10 1607 or later", ErrUnavailable)
			return
		}
		if _, err := containerSID(); err != nil {
			probeErr = fmt.Errorf("%w: can't create the AppContainer: %v", ErrUnavailable, err)
			return
		}
		if _, err := os.Executable(); err != nil {
			probeErr = fmt.Errorf("%w: %v", ErrUnavailable, err)
		}
	})
	return probeErr
}

var (
	pkgOnce sync.Once
	pkgSID  *windows.SID
	pkgErr  error
)

// containerSID creates ackn4th's AppContainer profile if needed and
// returns its SID.
func containerSID() (*windows.SID, error) {
	pkgOnce.Do(func() {
		if pkgErr = procCreateAppContainerProfile.Find(); pkgErr != nil {
			return
		}
		name, _ := windows.UTF16PtrFromString(containerName)
		desc, _ := windows.UTF16PtrFromString("ackn4th sandbox for agents' commands")
		var sid *windows.SID
		hr, _, _ := procCreateAppContainerProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(desc)), uintptr(unsafe.Pointer(desc)), 0, 0, uintptr(unsafe.Pointer(&sid)))
		if hr != 0 {
			// It exists already (or can't be created): derive its SID.
			if hr2, _, _ := procDeriveAppContainerSid.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid))); hr2 != 0 {
				pkgErr = fmt.Errorf("AppContainer profile %s: HRESULT 0x%08x", containerName, uint32(hr))
				return
			}
		}
		defer windows.FreeSid(sid)
		pkgSID, pkgErr = sid.Copy()
	})
	return pkgSID, pkgErr
}

var (
	capMu   sync.Mutex
	capSIDs = map[string]*windows.SID{}
)

// capability returns the SID of the capability with this name.
func capability(name string) (*windows.SID, error) {
	capMu.Lock()
	defer capMu.Unlock()
	if s, ok := capSIDs[name]; ok {
		return s, nil
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, err
	}
	var groups, caps **windows.SID
	var ng, nc uint32
	ok, _, err := procDeriveCapabilitySids.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(&groups)), uintptr(unsafe.Pointer(&ng)), uintptr(unsafe.Pointer(&caps)), uintptr(unsafe.Pointer(&nc)))
	if ok == 0 {
		return nil, fmt.Errorf("capability %s: %w", name, err)
	}
	free := func(arr **windows.SID, n uint32) {
		if arr == nil {
			return
		}
		for _, s := range unsafe.Slice(arr, n) {
			windows.LocalFree(windows.Handle(unsafe.Pointer(s)))
		}
		windows.LocalFree(windows.Handle(unsafe.Pointer(arr)))
	}
	defer free(groups, ng)
	defer free(caps, nc)
	if nc == 0 {
		return nil, fmt.Errorf("capability %s: no SID", name)
	}
	s, err := unsafe.Slice(caps, nc)[0].Copy()
	if err != nil {
		return nil, err
	}
	capSIDs[name] = s
	return s, nil
}

var (
	aclMu sync.Mutex
	aclOK = map[string]bool{} // grants known to be in place
	// toolTried holds tool directories already granted, or that can't be,
	// so neither is attempted again.
	toolTried sync.Map
	// toolGranted records, for each, whether the grant is in place.
	toolGranted sync.Map
)

// ensureACE makes sure path's ACL has an explicit entry of typ ("A" allow,
// "D" deny) for sid, inherited by everything under it.
func ensureACE(path string, sid *windows.SID, typ string, mask uint32) error {
	key := typ + "|" + sid.String() + "|" + strings.ToLower(path)
	aclMu.Lock()
	defer aclMu.Unlock()
	if aclOK[key] {
		return nil
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("reading the ACL of %s: %w", path, err)
	}
	if !hasExplicit(sd.String(), typ, sid.String()) {
		dacl, _, err := sd.DACL()
		if err != nil {
			return err
		}
		mode := windows.GRANT_ACCESS
		if typ == "D" {
			mode = windows.DENY_ACCESS
		}
		inherit := uint32(windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
		if !isDir(path) {
			inherit = windows.NO_INHERITANCE
		}
		acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
			AccessPermissions: windows.ACCESS_MASK(mask),
			AccessMode:        windows.ACCESS_MODE(mode),
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
				TrusteeValue: windows.TrusteeValueFromSID(sid),
			},
		}}, dacl)
		if err != nil {
			return err
		}
		info := windows.SECURITY_INFORMATION(windows.DACL_SECURITY_INFORMATION)
		// Keep a protected ACL protected: setting it otherwise turns
		// inheritance from the parent back on.
		if ctl, _, err := sd.Control(); err == nil && ctl&windows.SE_DACL_PROTECTED != 0 {
			info |= windows.PROTECTED_DACL_SECURITY_INFORMATION
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, info, nil, nil, acl, nil); err != nil {
			return fmt.Errorf("granting the sandbox access to %s: %w", path, err)
		}
	}
	aclOK[key] = true
	return nil
}

// ensureLowLabel labels path (and what's under it) low integrity, so
// AppContainer processes, which run at low integrity, can write there.
func ensureLowLabel(path string) error {
	key := "label|" + strings.ToLower(path)
	aclMu.Lock()
	defer aclMu.Unlock()
	if aclOK[key] {
		return nil
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION)
	if err != nil {
		return fmt.Errorf("reading the label of %s: %w", path, err)
	}
	if !hasLowLabel(sd.String()) {
		sddl := "S:(ML;OICI;NW;;;LW)"
		if !isDir(path) {
			sddl = "S:(ML;;NW;;;LW)"
		}
		label, err := windows.SecurityDescriptorFromString(sddl)
		if err != nil {
			return err
		}
		sacl, _, err := label.SACL()
		if err != nil {
			return err
		}
		if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.LABEL_SECURITY_INFORMATION, nil, nil, nil, sacl); err != nil {
			return fmt.Errorf("labeling %s for the sandbox: %w", path, err)
		}
	}
	aclOK[key] = true
	return nil
}

// spec is what the launcher needs to start the confined process.
type spec struct {
	Caps    []string `json:"caps"` // capability SIDs
	Network bool     `json:"net,omitempty"`
	Parent  int      `json:"parent"` // ackn4th's pid: the launcher stops if it goes
}

// maxCommandLine is Windows' limit on a command line, in characters.
const maxCommandLine = 32767

// wrap grants the sandbox what cfg allows and returns argv run by the
// launcher.
func wrap(cfg Config, argv []string) ([]string, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	argv, extra, err := resolveProgram(argv)
	if err != nil {
		return nil, err
	}
	var caps []string
	// Every grant besides the workspace's own, to protect where it
	// overlaps the workspace (a Keep or scratch directory that holds, or
	// sits inside, the workspace would otherwise expose .git and
	// credential files to its capability, which the deny entries on the
	// workspace's capability don't touch).
	type granted struct {
		sid  *windows.SID
		path string
	}
	var others []granted
	grant := func(kind, path string, mask uint32, writable bool) (*windows.SID, error) {
		sid, err := capability(capName(kind, path))
		if err != nil {
			return nil, err
		}
		if kind != "ws-rw" && kind != "ws-ro" {
			others = append(others, granted{sid, path})
		}
		if err := ensureACE(path, sid, "A", mask); err != nil {
			return nil, err
		}
		if writable {
			if err := ensureLowLabel(path); err != nil {
				return nil, err
			}
		}
		caps = append(caps, sid.String())
		return sid, nil
	}
	const rw = fileRead | fileWrite | fileExecute | fileDelete
	// The workspace.
	kind, mask := "ws-rw", uint32(rw)
	if cfg.ReadOnly {
		kind, mask = "ws-ro", fileRead|fileExecute
	}
	ws, err := grant(kind, cfg.Workspace, mask, !cfg.ReadOnly)
	if err != nil {
		return nil, err
	}
	var readOnlyPaths []string
	if !cfg.ReadOnly {
		for _, d := range []string{".git", ".ackn4th"} {
			if p := filepath.Join(cfg.Workspace, d); exists(p) {
				readOnlyPaths = append(readOnlyPaths, p)
				if err := ensureACE(p, ws, "D", denyWrite); err != nil {
					return nil, err
				}
			}
		}
	}
	// Credential files, and hidden paths, inside the workspace.
	unreadable, err := secretFiles(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	for _, h := range hidden(cfg) {
		if within(h, cfg.Workspace) && exists(h) {
			unreadable = append(unreadable, h)
		}
	}
	for _, p := range unreadable {
		if err := ensureACE(p, ws, "D", denyRead); err != nil {
			return nil, err
		}
	}
	// Scratch, and the CLI's own directories.
	scratch, err := grant("scratch", cfg.Scratch, rw, true)
	if err != nil {
		return nil, err
	}
	for _, f := range cfg.Fixed {
		if exists(f) {
			if err := ensureACE(f, scratch, "D", denyWrite); err != nil {
				return nil, err
			}
		}
	}
	for _, k := range cfg.Keep {
		if _, err := grant("keep", k, rw, true); err != nil {
			return nil, err
		}
	}
	// Where another grant covers part of the workspace, apply the same
	// protections to its capability.
	for _, o := range others {
		for _, p := range readOnlyPaths {
			if within(p, o.path) {
				if err := ensureACE(p, o.sid, "D", denyWrite); err != nil {
					return nil, err
				}
			}
		}
		for _, p := range unreadable {
			if within(p, o.path) {
				if err := ensureACE(p, o.sid, "D", denyRead); err != nil {
					return nil, err
				}
			}
		}
	}
	// Tools outside the system directories, read-only. Best effort: a
	// directory the user doesn't own can't be granted, and a tool there
	// fails to start.
	tools, err := capability("ackn4th.tools")
	if err != nil {
		return nil, err
	}
	hide := hidden(cfg)
	for _, d := range toolDirs(cfg, append(extra, argv[0])) {
		key := strings.ToLower(d)
		if _, tried := toolTried.LoadOrStore(key, true); !tried {
			toolGranted.Store(key, ensureACE(d, tools, "A", fileRead|fileExecute) == nil)
		}
		// Where a hidden path (credentials, ackn4th's own data) sits in a
		// tool directory, the grant must not reach it. Fail closed.
		if ok, _ := toolGranted.Load(key); ok == true {
			for _, h := range hide {
				if within(h, d) && exists(h) {
					if err := ensureACE(h, tools, "D", denyRead); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	caps = append(caps, tools.String())

	enc, err := json.Marshal(spec{Caps: caps, Network: cfg.Network, Parent: os.Getpid()})
	if err != nil {
		return nil, err
	}
	out := append([]string{exe, launcherArg, base64.RawURLEncoding.EncodeToString(enc)}, argv...)
	if n := len(windows.ComposeCommandLine(out)); n > maxCommandLine {
		return nil, fmt.Errorf("the command is too long for Windows: %d characters, at most %d", n, maxCommandLine)
	}
	return out, nil
}

// Program returns argv ready to start on Windows: argv[0] as an absolute
// path, and an npm .cmd shim replaced by node running its script.
func Program(argv []string) ([]string, error) {
	out, _, err := resolveProgram(argv)
	return out, err
}

// resolveProgram makes argv[0] an absolute path to a program Windows can
// start with these arguments. npm's .cmd shims (how Codex and Copilot
// install) run their script with node instead: cmd.exe can't pass
// arguments with newlines or quotes through safely. extra lists
// directories the program needs to read.
func resolveProgram(argv []string) (out, extra []string, err error) {
	if len(argv) == 0 {
		return nil, nil, errors.New("no command")
	}
	p := argv[0]
	if !filepath.IsAbs(p) {
		if p, err = exec.LookPath(p); err != nil {
			return nil, nil, err
		}
	}
	switch strings.ToLower(filepath.Ext(p)) {
	case ".cmd", ".bat":
		data, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, err
		}
		script := npmShimScript(string(data))
		if script == "" {
			return nil, nil, fmt.Errorf("%s is a batch file, which can't take arguments safely: run the program it starts directly", p)
		}
		dir := filepath.Dir(p)
		node := filepath.Join(dir, "node.exe")
		if !exists(node) {
			if node, err = exec.LookPath("node"); err != nil {
				return nil, nil, fmt.Errorf("%s needs node: %w", p, err)
			}
		}
		return append([]string{node, filepath.Join(dir, script)}, argv[1:]...), []string{dir}, nil
	case ".ps1":
		return nil, nil, fmt.Errorf("%s is a PowerShell script: run the program it starts directly", p)
	}
	return append([]string{p}, argv[1:]...), nil, nil
}

// toolDirs lists directories tools are run from that an AppContainer
// can't already read: PATH entries and toolchains outside the Windows and
// Program Files directories. The home directory, its ancestors and hidden
// paths are never included.
func toolDirs(cfg Config, programs []string) []string {
	home, _ := os.UserHomeDir()
	system := []string{os.Getenv("SystemRoot"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)")}
	hide := hidden(cfg)
	var out []string
	add := func(d string) {
		d = filepath.Clean(d)
		if !filepath.IsAbs(d) || !isDir(d) || d == filepath.Dir(d) {
			return
		}
		for _, s := range system {
			if s != "" && within(d, s) {
				return
			}
		}
		if home != "" && within(home, d) {
			return
		}
		// Not inside a hidden path. A hidden path inside d (a toolchain
		// directory that holds a credentials file, such as ~/.cargo) is
		// allowed: wrap denies it to the tools capability.
		for _, h := range hide {
			if within(d, h) {
				return
			}
		}
		// Nor a directory that holds the workspace, scratch or a Keep
		// directory: read access would reach everything beside them.
		if within(d, cfg.Workspace) || within(d, cfg.Scratch) || within(cfg.Workspace, d) || within(cfg.Scratch, d) {
			return
		}
		for _, k := range cfg.Keep {
			if within(k, d) {
				return
			}
		}
		if slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, d) }) {
			return
		}
		out = append(out, d)
	}
	for _, p := range programs {
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			add(p)
		} else if filepath.IsAbs(p) {
			add(filepath.Dir(p))
		}
	}
	for _, e := range filepath.SplitList(os.Getenv("PATH")) {
		add(e)
		// Scoop's shims start programs installed beside them.
		if strings.EqualFold(filepath.Base(e), "shims") && strings.EqualFold(filepath.Base(filepath.Dir(e)), "scoop") {
			add(filepath.Join(filepath.Dir(e), "apps"))
		}
	}
	for _, v := range toolchains(home) {
		add(v)
	}
	for _, k := range []string{"GOROOT", "JAVA_HOME"} {
		if v := os.Getenv(k); v != "" {
			add(v)
		}
	}
	return out
}

// within reports whether p is root or under it, ignoring case as Windows
// does.
func within(p, root string) bool {
	p, root = strings.ToLower(filepath.Clean(p)), strings.ToLower(filepath.Clean(root))
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, `\`)+`\`)
}

func exists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

// Loopback reports whether sandboxed processes can connect to ackn4th on
// localhost, as agent CLIs do to reach its tools. Windows blocks loopback
// for AppContainers unless the container is exempted, which takes an
// administrator once.
func Loopback() error {
	pkg, err := containerSID()
	if err != nil || procNetIsoGetConfig.Find() != nil {
		return nil // can't tell; starting the CLI will
	}
	var n uint32
	var arr *windows.SIDAndAttributes
	if r, _, _ := procNetIsoGetConfig.Call(uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&arr))); r != 0 {
		return nil
	}
	if arr != nil {
		for _, s := range unsafe.Slice(arr, n) {
			if s.Sid != nil && s.Sid.String() == pkg.String() {
				return nil
			}
		}
	}
	return fmt.Errorf("Windows keeps sandboxed programs off localhost, which agent CLIs use to reach ackn4th: allow it once, from a terminal run as administrator: CheckNetIsolation LoopbackExempt -a -p=%s", pkg)
}

// launch runs in the launcher process: it starts argv in the AppContainer
// and a job object, waits, and returns its exit code.
func launch(enc string, argv []string) int {
	fail := func(err error) int {
		fmt.Fprintln(os.Stderr, "ackn4th sandbox:", err)
		return 125
	}
	if len(argv) == 0 {
		return fail(errors.New("no command"))
	}
	raw, err := base64.RawURLEncoding.DecodeString(enc)
	if err != nil {
		return fail(err)
	}
	var sp spec
	if err := json.Unmarshal(raw, &sp); err != nil {
		return fail(err)
	}
	pkg, err := containerSID()
	if err != nil {
		return fail(err)
	}
	names := sp.Caps
	if sp.Network {
		names = append(names, networkCaps...)
	}
	var caps []windows.SIDAndAttributes
	for _, s := range names {
		sid, err := windows.StringToSid(s)
		if err != nil {
			return fail(err)
		}
		caps = append(caps, windows.SIDAndAttributes{Sid: sid, Attributes: windows.SE_GROUP_ENABLED})
	}
	sc := struct {
		AppContainerSid *windows.SID
		Capabilities    *windows.SIDAndAttributes
		CapabilityCount uint32
		Reserved        uint32
	}{AppContainerSid: pkg, CapabilityCount: uint32(len(caps))}
	if len(caps) > 0 {
		sc.Capabilities = &caps[0]
	}

	// The job ends everything the command starts when the launcher exits
	// or is killed.
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fail(err)
	}
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fail(err)
	}
	// Cut the confined tree off from the desktop: no clipboard (it may
	// hold a password or token), no handles to other windows, no desktop
	// switching, no changing system parameters, no logging off.
	ui := windows.JOBOBJECT_BASIC_UI_RESTRICTIONS{UIRestrictionsClass: windows.JOB_OBJECT_UILIMIT_READCLIPBOARD |
		windows.JOB_OBJECT_UILIMIT_WRITECLIPBOARD | windows.JOB_OBJECT_UILIMIT_HANDLES | windows.JOB_OBJECT_UILIMIT_DESKTOP |
		windows.JOB_OBJECT_UILIMIT_EXITWINDOWS | windows.JOB_OBJECT_UILIMIT_GLOBALATOMS | windows.JOB_OBJECT_UILIMIT_SYSTEMPARAMETERS |
		windows.JOB_OBJECT_UILIMIT_DISPLAYSETTINGS}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui))); err != nil {
		return fail(err)
	}

	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return fail(err)
	}
	defer attrs.Delete()
	if err := attrs.Update(procThreadAttributeSecurityCapabilities, unsafe.Pointer(&sc), unsafe.Sizeof(sc)); err != nil {
		return fail(err)
	}
	si := windows.StartupInfoEx{}
	si.Cb = uint32(unsafe.Sizeof(si))
	si.Flags = windows.STARTF_USESTDHANDLES
	// Only the standard handles are inherited.
	var handles []windows.Handle
	for i, k := range []uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
		h, err := windows.GetStdHandle(k)
		if err != nil || h == 0 || h == windows.InvalidHandle {
			continue
		}
		*[]*windows.Handle{&si.StdInput, &si.StdOutput, &si.StdErr}[i] = h
		if windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT) == nil && !slices.Contains(handles, h) {
			handles = append(handles, h)
		}
	}
	if len(handles) > 0 {
		if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
			return fail(err)
		}
	}
	si.ProcThreadAttributeList = attrs.List()

	line, err := windows.UTF16PtrFromString(commandLine(argv))
	if err != nil {
		return fail(err)
	}
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(nil, line, nil, nil, len(handles) > 0, flags, nil, nil, &si.StartupInfo, &pi); err != nil {
		return fail(fmt.Errorf("starting %s: %w", argv[0], err))
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return fail(err)
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		windows.TerminateProcess(pi.Process, 1)
		return fail(err)
	}
	wait := []windows.Handle{pi.Process}
	if sp.Parent > 0 {
		if h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(sp.Parent)); err == nil {
			defer windows.CloseHandle(h)
			wait = append(wait, h)
		}
	}
	ev, err := windows.WaitForMultipleObjects(wait, false, windows.INFINITE)
	if err != nil {
		return fail(err)
	}
	if ev != windows.WAIT_OBJECT_0 {
		return 1 // ackn4th is gone: exiting closes the job, ending the command
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return fail(err)
	}
	return int(code)
}

// commandLine joins argv for CreateProcess. The shell's command goes to
// cmd.exe as is: with /s, cmd runs what's between the outer quotes.
func commandLine(argv []string) string {
	if len(argv) == 5 && argv[1] == "/d" && argv[2] == "/s" && argv[3] == "/c" && strings.EqualFold(filepath.Base(argv[0]), "cmd.exe") {
		return windows.EscapeArg(argv[0]) + ` /d /s /c "` + argv[4] + `"`
	}
	return windows.ComposeCommandLine(argv)
}
