// EPIC-048 M3: tsnet fallback-to-local rule and tsnetStart test seam.
package main

import (
	"context"
	"log"
	"net"
)

// tsnetFallbackWarn is the WARN message emitted when tsnet is default-enabled
// but no tsnet_authkey is resolvable and the operator did not explicitly opt in.
//
// Format is golden-tested in EPIC-048 M3  -  do not change wording.
const tsnetFallbackWarn = "WARN: tsnet default enabled but no tsnet_authkey resolvable; " +
	"falling back to --local. Set tsnet_authkey in server.yaml or pass --tsnet-authkey to force tsnet."

// applyTsnetFallback implements the EPIC-048 fallback-to-local rule.
//
// Returns the adjusted tsnetEnabled flag. If tsnet was default-enabled (not
// explicit) and no authkey is available, tsnet is disabled and the pinned WARN
// message is written to logger.
//
// Five conditions are checked:
//  1. tsnetEnabled=false → already local, no-op.
//  2. tsnetExplicit=true → operator explicitly opted in/out, respect it.
//  3. authKey non-empty → tsnet can start, no-op.
//  4. All others → default-on tsnet with no authkey: emit WARN, return false.
//
// logger must use no timestamp prefix (log.New(w, "", 0)) so the WARN text is
// stable for the golden test.
func applyTsnetFallback(tsnetEnabled, tsnetExplicit bool, authKey, clientSecret string, logger *log.Logger) bool {
	if !tsnetEnabled || tsnetExplicit || authKey != "" || clientSecret != "" {
		return tsnetEnabled
	}
	logger.Print(tsnetFallbackWarn)
	return false
}

// tsnetStartFunc is the injectable seam for tsnet bring-up. Integration tests
// swap it for a local TCP listener to avoid the real Tailscale control plane.
type tsnetStartFunc func(ctx context.Context, cfg TsnetConfig) (ln net.Listener, cleanup func() error, fqdn string, err error)

// serveDeps carries the injectable dependencies of `linkari serve`, threaded
// through serveCmdWith instead of package-level seams (EPIC-258 M2). A zero
// serveDeps is the production configuration: resolve() fills every nil field
// with its real implementation.
type serveDeps struct {
	tsnetStart tsnetStartFunc
}

// resolve returns a copy with production defaults substituted for nil fields.
func (d serveDeps) resolve() serveDeps {
	if d.tsnetStart == nil {
		d.tsnetStart = realTsnetStart
	}
	return d
}

// realTsnetStart is the production implementation: creates a TsnetServer,
// starts it, and returns the Funnel listener, a Close func, and the FQDN.
func realTsnetStart(ctx context.Context, cfg TsnetConfig) (net.Listener, func() error, string, error) {
	s := NewTsnetServer(cfg)
	ln, err := s.Start(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	return ln, s.Close, s.FQDN(), nil
}
