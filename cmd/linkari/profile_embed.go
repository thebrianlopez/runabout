package main

import (
	"embed"
	"io/fs"
)

//go:embed profiles/*.yaml profiles/*.md
var embeddedProfiles embed.FS

func EmbeddedProfileFS() fs.FS {
	sub, _ := fs.Sub(embeddedProfiles, "profiles")
	return sub
}
