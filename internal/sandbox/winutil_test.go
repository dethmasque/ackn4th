package sandbox

import "testing"

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
