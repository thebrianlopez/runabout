package main

import (
	"errors"
	"net/url"
	"strings"
)

var errUnsupportedScheme = errors.New("unsupported URL scheme")

// youtubeHosts is the exclusion list — the router never intercepts these.
var youtubeHosts = map[string]bool{
	"youtube.com":     true,
	"www.youtube.com": true,
	"youtu.be":        true,
	"m.youtube.com":   true,
}

// MatchHost extracts the registered hostname from rawURL for router key lookup.
// Strips "www." prefix. Returns ("", errUnsupportedScheme) for non-http(s) URLs.
func MatchHost(rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errUnsupportedScheme
	}
	host := u.Hostname()
	host = strings.TrimPrefix(host, "www.")
	return host, nil
}

// IsYouTube returns true if the URL's host is in the YouTube exclusion list.
func IsYouTube(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return youtubeHosts[u.Hostname()]
}
