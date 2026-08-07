package client

import (
	"testing"
)

func TestNew_ValidInputs(t *testing.T) {
	c, err := New("test-token", "owner", "repo")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if c.Owner() != "owner" {
		t.Errorf("Owner: got %q, want %q", c.Owner(), "owner")
	}
	if c.Repo() != "repo" {
		t.Errorf("Repo: got %q, want %q", c.Repo(), "repo")
	}
	c.Close()
}

func TestNew_EmptyToken(t *testing.T) {
	_, err := New("", "owner", "repo")
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestNew_EmptyOwner(t *testing.T) {
	_, err := New("token", "", "repo")
	if err == nil {
		t.Fatal("expected error for empty owner")
	}
}

func TestNew_EmptyRepo(t *testing.T) {
	_, err := New("token", "owner", "")
	if err == nil {
		t.Fatal("expected error for empty repo")
	}
}
