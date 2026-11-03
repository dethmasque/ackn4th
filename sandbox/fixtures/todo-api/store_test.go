package main

import "testing"

func TestAddAndComplete(t *testing.T) {
	s := NewStore()
	a := s.Add("write tests")
	if a.ID != 1 || a.Done {
		t.Fatalf("Add = %+v", a)
	}
	if _, err := s.Complete(a.ID); err != nil {
		t.Fatal(err)
	}
	if got := s.List(); len(got) != 1 || !got[0].Done {
		t.Fatalf("List = %+v", got)
	}
	if _, err := s.Complete(42); err != ErrNotFound {
		t.Fatalf("Complete(42) err = %v", err)
	}
}
