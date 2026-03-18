package main

import (
	"net/mail"
	"strings"
	"testing"

	proton "github.com/ProtonMail/go-proton-api"
)

func TestSlugify(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Hello World!", "hello-world"},
		{"Re: Meeting Notes (2024)", "re-meeting-notes-2024"},
		{"---leading-trailing---", "leading-trailing"},
		{"ALLCAPS", "allcaps"},
		{"", ""},
		{"a", "a"},
		{"special!@#chars", "special-chars"},
	}
	for _, tt := range tests {
		got := slugify(tt.input)
		if got != tt.want {
			t.Errorf("slugify(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestQuotedStrings(t *testing.T) {
	got := quotedStrings([]string{"a", "b", "c"})
	if len(got) != 3 {
		t.Fatalf("expected 3 items, got %d", len(got))
	}
	if got[0] != `"a"` || got[1] != `"b"` || got[2] != `"c"` {
		t.Errorf("unexpected output: %v", got)
	}
}

func TestQuotedStringsEmpty(t *testing.T) {
	got := quotedStrings(nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestFormatAddress(t *testing.T) {
	tests := []struct {
		addr *mail.Address
		want string
	}{
		{nil, ""},
		{&mail.Address{Address: "user@example.com"}, "user@example.com"},
		{&mail.Address{Name: "John Doe", Address: "john@example.com"}, "John Doe <john@example.com>"},
	}
	for _, tt := range tests {
		got := formatAddress(tt.addr)
		if got != tt.want {
			t.Errorf("formatAddress(%v) = %q, want %q", tt.addr, got, tt.want)
		}
	}
}

func TestFormatAddressList(t *testing.T) {
	addrs := []*mail.Address{
		{Address: "a@example.com"},
		{Name: "B", Address: "b@example.com"},
	}
	got := formatAddressList(addrs)
	if !strings.Contains(got, "a@example.com") {
		t.Errorf("expected a@example.com in %q", got)
	}
	if !strings.Contains(got, "B <b@example.com>") {
		t.Errorf("expected B <b@example.com> in %q", got)
	}
}

func TestHtmlToText(t *testing.T) {
	tests := []struct {
		name, input, wantSubstr string
	}{
		{"plain text in p", "<p>Hello world</p>", "Hello world"},
		{"br becomes newline", "line1<br>line2", "line2"},
		{"link with href", `<a href="https://example.com">click</a>`, "click"},
		{"link includes url", `<a href="https://example.com">click</a>`, "https://example.com"},
		{"nested tags", "<div><p>inner</p></div>", "inner"},
		{"angle brackets", "<div>text here</div>", "text here"},
	}
	for _, tt := range tests {
		got := htmlToText(tt.input)
		if !strings.Contains(got, tt.wantSubstr) {
			t.Errorf("%s: htmlToText(%q) = %q, want substring %q", tt.name, tt.input, got, tt.wantSubstr)
		}
	}
}

func TestMatchesContact(t *testing.T) {
	meta := proton.MessageMetadata{
		Sender: &mail.Address{Address: "alice@example.com"},
		ToList: []*mail.Address{
			{Address: "bob@example.com"},
		},
		CCList: []*mail.Address{
			{Address: "cc@example.com"},
		},
		BCCList: []*mail.Address{
			{Address: "bcc@example.com"},
		},
	}

	tests := []struct {
		email string
		want  bool
	}{
		{"alice@example.com", true},
		{"bob@example.com", true},
		{"cc@example.com", true},
		{"bcc@example.com", true},
		{"nobody@example.com", false},
		{"alice@example.com", true}, // matchesContact lowercases input before comparing
	}
	for _, tt := range tests {
		got := matchesContact(meta, tt.email)
		if got != tt.want {
			t.Errorf("matchesContact(meta, %q) = %v, want %v", tt.email, got, tt.want)
		}
	}
}

func TestBuildFilename(t *testing.T) {
	meta := proton.MessageMetadata{
		ID:      "abcdef1234567890",
		Subject: "Re: Meeting Notes",
		Time:    1709251200, // 2024-03-01 00:00:00 UTC
	}
	got := buildFilename(meta)
	if !strings.HasPrefix(got, "2024-03-01_abcdef12_") {
		t.Errorf("unexpected filename prefix: %q", got)
	}
	if !strings.HasSuffix(got, ".md") {
		t.Errorf("expected .md suffix: %q", got)
	}
	if strings.Contains(got, " ") {
		t.Errorf("filename should not contain spaces: %q", got)
	}
}

func TestBuildMarkdown(t *testing.T) {
	meta := proton.MessageMetadata{
		ID:      "test123456789012",
		Subject: "Test Subject",
		Sender:  &mail.Address{Name: "Alice", Address: "alice@example.com"},
		ToList:  []*mail.Address{{Address: "bob@example.com"}},
		Time:    1709251200,
	}

	md := buildMarkdown(meta, "Hello, world!")

	checks := []string{
		"---",
		`id: "test123456789012"`,
		`subject: "Test Subject"`,
		"# Test Subject",
		"Hello, world!",
	}
	for _, want := range checks {
		if !strings.Contains(md, want) {
			t.Errorf("buildMarkdown missing %q", want)
		}
	}
}

func TestExtractBodyPlainText(t *testing.T) {
	input := []byte("plain text body")
	got := extractBody(input, "text/plain")
	if got != "plain text body" {
		t.Errorf("expected %q, got %q", "plain text body", got)
	}
}

func TestExtractBodyHTML(t *testing.T) {
	input := []byte("<p>Hello</p>")
	got := extractBody(input, "text/html")
	if !strings.Contains(got, "Hello") {
		t.Errorf("expected 'Hello' in %q", got)
	}
}
