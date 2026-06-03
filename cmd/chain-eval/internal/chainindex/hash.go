package chainindex

// ComputeContentHash returns "sha256:<hex>" computed over all .md files under
// docsRoot. The preimage is: for each relative path in sorted lexicographic order,
//   "{relative_path}\t{size_bytes}\t{mtime_nanoseconds}\n"
// No file content is read - stat metadata only.
func ComputeContentHash(docsRoot string) (string, error) {
	panic("not implemented")
}

// VerifyContentHash re-computes the hash over current filesystem state and
// returns (true, nil) when it matches storedHash. Returns (false, nil) on
// mismatch (stale), never an error on mismatch alone.
func VerifyContentHash(storedHash, docsRoot string) (bool, error) {
	panic("not implemented")
}
