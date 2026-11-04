package sandbox

import (
	"strings"
	"testing"
)

const sampleSDDL = `O:S-1-5-21-1-2-3-1001G:S-1-5-21-1-2-3-513D:AI(D;OICI;0x10156;;;S-1-15-3-1024-9)(A;OICI;0x1301bf;;;S-1-15-3-1024-7)(A;OICIID;FA;;;SY)(A;OICIID;FA;;;S-1-5-21-1-2-3-1001)(A;OICIID;0x1200a9;;;S-1-15-3-1024-8)S:(ML;OICI;NW;;;LW)`

func TestParseACEs(t *testing.T) {
	a := parseACEs(sampleSDDL)
	if len(a) != 5 {
		t.Fatalf("got %d entries: %+v", len(a), a)
	}
	if a[0].Type != "D" || a[0].Inherited || a[0].SID != "S-1-15-3-1024-9" || a[0].Rights != "0x10156" {
		t.Errorf("first entry: %+v", a[0])
	}
	if !a[2].Inherited || a[2].SID != "SY" {
		t.Errorf("third entry: %+v", a[2])
	}
	if !hasExplicit(sampleSDDL, "A", "S-1-15-3-1024-7") || !hasExplicit(sampleSDDL, "D", "s-1-15-3-1024-9") {
		t.Error("explicit entries not found")
	}
	if hasExplicit(sampleSDDL, "A", "S-1-15-3-1024-8") {
		t.Error("an inherited entry counted as explicit")
	}
	if hasExplicit(sampleSDDL, "A", "S-1-15-3-1024-9") {
		t.Error("a deny entry counted as allow")
	}
}

func TestHasLowLabel(t *testing.T) {
	if !hasLowLabel(sampleSDDL) || !hasLowLabel("S:(ML;OICIID;NW;;;LW)") || !hasLowLabel("S:(ML;;NW;;;LW)") {
		t.Error("low labels not found")
	}
	if hasLowLabel("S:(ML;OICI;NW;;;ME)") || hasLowLabel("D:(A;;FA;;;LW)") {
		t.Error("not low labels")
	}
}

func TestNPMShimScript(t *testing.T) {
	shim := "@ECHO off\r\nGOTO start\r\n:find_dp0\r\nSET dp0=%~dp0\r\nEXIT /b\r\n:start\r\nSETLOCAL\r\nCALL :find_dp0\r\n\r\n" +
		"IF EXIST \"%dp0%\\node.exe\" (\r\n  SET \"_prog=%dp0%\\node.exe\"\r\n) ELSE (\r\n  SET \"_prog=node\"\r\n  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n)\r\n\r\n" +
		"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & \"%_prog%\"  \"%dp0%\\node_modules\\@github\\copilot\\npm-loader.js\" %*\r\n"
	if got := npmShimScript(shim); got != `node_modules\@github\copilot\npm-loader.js` {
		t.Errorf("got %q", got)
	}
	if got := npmShimScript("@echo off\r\n\"%~dp0\\tool.exe\" %*\r\n"); got != "" {
		t.Errorf("not a shim, got %q", got)
	}
}

func TestCapName(t *testing.T) {
	if capName("ws", `C:\Code\App`) != capName("ws", `c:\code\app`) {
		t.Error("capability names should ignore case, as Windows paths do")
	}
	if capName("ws", `C:\a`) == capName("ws", `C:\b`) || capName("ws", `C:\a`) == capName("scratch", `C:\a`) {
		t.Error("capability names should differ by path and kind")
	}
}

// The workspace's grant (inherited), an old deny entry from an earlier
// version, and ALL APPLICATION PACKAGES: restricting .git keeps the user's
// own entries and gives the sandbox read-only access, nothing more.
func TestRestrictDACL(t *testing.T) {
	const ws, ro, keep = "S-1-15-3-1024-7", "S-1-15-3-1024-8", "S-1-15-3-1024-9"
	in := `O:S-1-5-21-1-2-3-1001D:AI(D;OICI;0x10156;;;` + ws + `)(A;OICI;0x1200a9;;;AC)(A;OICIID;FA;;;SY)(A;OICIID;FA;;;S-1-5-21-1-2-3-1001)(A;OICIID;0x1301bf;;;` + ws + `)(A;OICIID;0x1301bf;;;` + keep + `)S:(ML;OICI;NW;;;LW)`
	got, changed, err := restrictDACL(in, []string{ws, ro}, true)
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	want := `D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;S-1-5-21-1-2-3-1001)(A;OICI;0x1200a9;;;` + ws + `)(A;OICI;0x1200a9;;;` + ro + `)`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// Applied again, nothing changes: no rewrite on every launch.
	if again, changed, err := restrictDACL(got, []string{ws, ro}, true); err != nil || changed || again != got {
		t.Errorf("second pass: changed=%v err=%v\n%s", changed, err, again)
	}
	// A credential file: no AppContainer access at all.
	got, _, err = restrictDACL(`D:AI(A;;FA;;;S-1-5-21-1-2-3-1001)(A;ID;0x1301bf;;;`+ws+`)`, nil, false)
	if err != nil || got != `D:PAI(A;;FA;;;S-1-5-21-1-2-3-1001)` {
		t.Errorf("secret: %s %v", got, err)
	}
	// Tampering (a command re-granting itself more) is undone.
	tampered := `D:PAI(A;;FA;;;S-1-5-21-1-2-3-1001)(A;OICI;FA;;;` + ws + `)`
	if got, changed, _ := restrictDACL(tampered, []string{ws}, true); !changed || strings.Contains(got, ";FA;;;"+ws) {
		t.Errorf("tampered grant kept: %s", got)
	}
	// Entries it can't copy faithfully are refused, not dropped.
	if _, _, err := restrictDACL(`D:(XA;;FA;;;WD;(@User.x == 1))`, nil, false); err == nil {
		t.Error("a conditional entry was accepted")
	}
}
