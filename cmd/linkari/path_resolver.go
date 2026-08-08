package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/adrg/xdg"
)

const linkariAppName = "linkari"

// PathRoots are the platform roots used by Linkari.
type PathRoots struct {
	Config string
	Data   string
	Cache  string
	State  string
}

// EffectivePaths are the resolved platform paths for the current config.
type EffectivePaths struct {
	ConfigDir      string
	DataDir        string
	CacheDir       string
	StateDir       string
	QueueDB        string
	SnapshotPath   string
	TranscriptsDir string
}

// PathResolver resolves Linkari's platform roots and effective paths.
type PathResolver interface {
	Roots() (PathRoots, error)
	Resolve(server ServerConfig) (EffectivePaths, error)
}

var activePathResolver PathResolver = xdgPathResolver{}

type xdgPathResolver struct{}

func (xdgPathResolver) Roots() (PathRoots, error) {
	xdg.Reload()
	return PathRoots{
		Config: filepath.Join(xdg.ConfigHome, linkariAppName),
		Data:   filepath.Join(xdg.DataHome, linkariAppName),
		Cache:  filepath.Join(xdg.CacheHome, linkariAppName),
		State:  filepath.Join(xdg.StateHome, linkariAppName),
	}, nil
}

func (r xdgPathResolver) Resolve(server ServerConfig) (EffectivePaths, error) {
	roots, err := r.Roots()
	if err != nil {
		return EffectivePaths{}, err
	}

	paths := EffectivePaths{
		ConfigDir: roots.Config,
		DataDir:   roots.Data,
		CacheDir:  roots.Cache,
		StateDir:  roots.State,
	}
	if server.DataDir != "" {
		paths.DataDir = expandTilde(server.DataDir)
	}
	if server.QueueDB != "" {
		paths.QueueDB = expandTilde(server.QueueDB)
	} else {
		paths.QueueDB = filepath.Join(paths.DataDir, "queue.db")
	}
	if server.SnapshotPath != "" {
		paths.SnapshotPath = expandTilde(server.SnapshotPath)
	} else {
		paths.SnapshotPath = filepath.Join(paths.DataDir, "backups", "latest.db")
	}
	if server.TranscriptsDir != "" {
		paths.TranscriptsDir = expandTilde(server.TranscriptsDir)
	} else {
		paths.TranscriptsDir = filepath.Join(paths.DataDir, "transcripts")
	}
	return paths, nil
}

func resolveEffectivePaths(cfg *ServerConfig) (EffectivePaths, error) {
	if cfg == nil {
		return activePathResolver.Resolve(ServerConfig{})
	}
	return activePathResolver.Resolve(*cfg)
}

func nativeConfigPath() string {
	roots, err := activePathResolver.Roots()
	if err != nil {
		return filepath.Join(os.TempDir(), linkariAppName, "config.toml")
	}
	return filepath.Join(roots.Config, "config.toml")
}

func legacyConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", linkariAppName, "config.toml")
}

type configPathResolution struct {
	Path   string
	Legacy bool
}

func resolveConfigPath(preferred string) configPathResolution {
	if preferred != "" {
		return configPathResolution{Path: expandTilde(preferred)}
	}

	native := nativeConfigPath()
	if _, err := os.Stat(native); err == nil {
		return configPathResolution{Path: native}
	}

	legacy := legacyConfigPath()
	if _, err := os.Stat(legacy); err == nil {
		return configPathResolution{Path: legacy, Legacy: true}
	}

	return configPathResolution{Path: native}
}

func configPathFromResolution(res configPathResolution) string {
	if res.Path != "" {
		return res.Path
	}
	return nativeConfigPath()
}

func ensureDir(path string) error {
	if path == "" {
		return nil
	}
	return os.MkdirAll(path, 0o700)
}

func ensureParentDir(path string) error {
	if path == "" {
		return nil
	}
	return ensureDir(filepath.Dir(path))
}

func legacyConfigLocationMessage(nativePath, legacyPath string) string {
	return fmt.Sprintf("legacy config at %s detected; migrate to %s with 'linkari config init' and copy your settings manually", legacyPath, nativePath)
}
