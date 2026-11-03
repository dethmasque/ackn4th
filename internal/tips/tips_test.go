package tips

import "testing"

func TestBook(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(Env, "")
	b := Load(dir, true)
	if !b.Pending("a") {
		t.Fatal("a fresh book should show every tip")
	}
	b.Done("a")
	if Load(dir, true).Pending("a") {
		t.Error("a seen tip should stay seen")
	}
	if !Load(dir, true).Pending("b") {
		t.Error("other tips should still show")
	}
	if Load(dir, false).Pending("b") {
		t.Error("tips: false should hide tips")
	}
	t.Setenv(Env, "1")
	if Load(dir, true).Pending("b") {
		t.Error(Env + "=1 should hide tips")
	}
	t.Setenv(Env, "")
	if err := Reset(dir); err != nil {
		t.Fatal(err)
	}
	if !Load(dir, true).Pending("a") {
		t.Error("reset should bring tips back")
	}
	var nilBook *Book
	if nilBook.Pending("a") || Off().Pending("a") {
		t.Error("nil and Off books show nothing")
	}
}
