package main

import (
	"embed"
	"io/fs"
)

//go:embed profiles/*.yaml
var embeddedProfiles embed.FS

func EmbeddedProfileFS() fs.FS {
	sub, _ := fs.Sub(embeddedProfiles, "profiles")
	return sub
}
