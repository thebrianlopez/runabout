package main

// testmain_test.go: package-level test setup.
// Pre-populates the archiveThresholdCfg cache with builtinConfig() before any test runs.
// This prevents the first call to loadArchiveThresholdConfig() from making slow AWS IMDS
// network calls (via expandConfigRefs/secrets.DefaultAWSFactory), which would hold
// archiveThresholdMu write lock for several seconds and cause scoreAsync goroutines
// in concurrent tests to time out.
// Pattern established in affb91c; extended here for global fix.
func init() {
	archiveThresholdMu.Lock()
	archiveThresholdCfg = builtinConfig()
	archiveThresholdMu.Unlock()
}
