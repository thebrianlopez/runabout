package chainindex

// Build constructs a ChainIndex from a flat ArtifactRecord slice.
// Chain key = FDD filename stem normalized (lowercase, underscores, prefix stripped).
// Orphan threshold is hardcoded to 2026-04-21; pass includeLegacy=true to include
// pre-threshold artifacts in the orphan list.
func Build(records []ArtifactRecord, docsRoot string, includeLegacy bool) ChainIndex {
	panic("not implemented")
}
