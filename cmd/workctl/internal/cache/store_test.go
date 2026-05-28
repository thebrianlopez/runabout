package cache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tempDB(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s := Open(filepath.Join(dir, "test.db"))
	if s == nil {
		t.Fatal("Open returned nil")
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenCreatesDir(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "a", "b", "c", "test.db")
	s := Open(nested)
	if s == nil {
		t.Fatal("Open returned nil for nested path")
	}
	s.Close()
}

func TestOpenGracefulDegradation(t *testing.T) {
	// Unwritable path should return nil, not panic.
	s := Open("/dev/null/impossible/path/cache.db")
	if s != nil {
		s.Close()
		t.Fatal("expected nil Store for invalid path")
	}
}

func TestPutGetRoundtrip(t *testing.T) {
	s := tempDB(t)
	data := []byte(`{"foo":"bar"}`)

	if err := s.Put("k1", "test", data, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get = %q, want %q", got, data)
	}
}

func TestGetMiss(t *testing.T) {
	s := tempDB(t)

	got, err := s.Get("nonexistent")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(nonexistent) = %q, want nil", got)
	}
}

func TestGetExpired(t *testing.T) {
	s := tempDB(t)
	data := []byte(`"expired"`)

	// Put with 0 TTL (already expired).
	if err := s.Put("k1", "test", data, 0); err != nil {
		t.Fatalf("Put: %v", err)
	}

	// Allow tiny delay for clock to advance past expires_at
	time.Sleep(time.Millisecond)

	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(expired) = %q, want nil", got)
	}
}

func TestPutUpsert(t *testing.T) {
	s := tempDB(t)

	if err := s.Put("k1", "test", []byte(`"v1"`), time.Hour); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := s.Put("k1", "test", []byte(`"v2"`), time.Hour); err != nil {
		t.Fatalf("Put v2: %v", err)
	}

	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != `"v2"` {
		t.Errorf("Get after upsert = %q, want %q", got, `"v2"`)
	}
}

func TestHasValid_Hit(t *testing.T) {
	s := tempDB(t)
	s.Put("k1", "test", []byte(`"val"`), time.Hour)

	if !s.HasValid("k1") {
		t.Error("HasValid(k1) = false, want true")
	}
}

func TestHasValid_Miss(t *testing.T) {
	s := tempDB(t)

	if s.HasValid("nonexistent") {
		t.Error("HasValid(nonexistent) = true, want false")
	}
}

func TestHasValid_Expired(t *testing.T) {
	s := tempDB(t)
	s.Put("k1", "test", []byte(`"old"`), 0)
	time.Sleep(time.Millisecond)

	if s.HasValid("k1") {
		t.Error("HasValid(expired) = true, want false")
	}
}

func TestDelete(t *testing.T) {
	s := tempDB(t)

	if err := s.Put("k1", "test", []byte(`"val"`), time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Delete("k1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get after Delete = %q, want nil", got)
	}
}

func TestClearAll(t *testing.T) {
	s := tempDB(t)

	for _, k := range []string{"a", "b", "c"} {
		s.Put(k, "test", []byte(`"x"`), time.Hour)
	}

	if err := s.Clear("", 0); err != nil {
		t.Fatalf("Clear: %v", err)
	}

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalEntries != 0 {
		t.Errorf("TotalEntries after Clear = %d, want 0", stats.TotalEntries)
	}
}

func TestClearBySource(t *testing.T) {
	s := tempDB(t)

	s.Put("j1", "jira", []byte(`"j"`), time.Hour)
	s.Put("j2", "jira", []byte(`"j"`), time.Hour)
	s.Put("c1", "confluence", []byte(`"c"`), time.Hour)

	if err := s.Clear("jira", 0); err != nil {
		t.Fatalf("Clear(jira): %v", err)
	}

	stats, _ := s.GetStats()
	if stats.TotalEntries != 1 {
		t.Errorf("TotalEntries after Clear(jira) = %d, want 1", stats.TotalEntries)
	}
	if _, ok := stats.BySource["jira"]; ok {
		t.Error("jira source still present after Clear(jira)")
	}
}

func TestPrune(t *testing.T) {
	s := tempDB(t)

	s.Put("expired", "test", []byte(`"old"`), 0)
	time.Sleep(time.Millisecond)
	s.Put("fresh", "test", []byte(`"new"`), time.Hour)

	if err := s.Prune(); err != nil {
		t.Fatalf("Prune: %v", err)
	}

	stats, _ := s.GetStats()
	if stats.TotalEntries != 1 {
		t.Errorf("TotalEntries after Prune = %d, want 1", stats.TotalEntries)
	}
}

func TestGetStats(t *testing.T) {
	s := tempDB(t)

	s.Put("j1", "jira", []byte(`"hello"`), time.Hour)
	s.Put("g1", "github_events", []byte(`"world"`), time.Hour)

	stats, err := s.GetStats()
	if err != nil {
		t.Fatalf("GetStats: %v", err)
	}
	if stats.TotalEntries != 2 {
		t.Errorf("TotalEntries = %d, want 2", stats.TotalEntries)
	}
	if len(stats.BySource) != 2 {
		t.Errorf("len(BySource) = %d, want 2", len(stats.BySource))
	}
}

func TestCompressionRoundtrip(t *testing.T) {
	data := []byte(`{"large": "` + string(make([]byte, 10000)) + `"}`)
	compressed, err := compress(data)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	decompressed, err := decompress(compressed)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if string(decompressed) != string(data) {
		t.Error("compression roundtrip mismatch")
	}
	// Compressed should be smaller than original for repetitive data.
	if len(compressed) >= len(data) {
		t.Errorf("compressed (%d) not smaller than original (%d)", len(compressed), len(data))
	}
}

func TestCloseNilStore(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Errorf("Close(nil) = %v, want nil", err)
	}
}

func TestDecompressEnforcesLimit(t *testing.T) {
	// Build a payload that decompresses to exactly maxDecompressBytes+1 bytes.
	// Zeros compress to a tiny gzip stream, so the compressed form is small.
	big := make([]byte, maxDecompressBytes+1)
	compressed, err := compress(big)
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	_, err = decompress(compressed)
	if err == nil {
		t.Fatal("decompress: expected error for oversized payload, got nil")
	}
}

func BenchmarkPutGet(b *testing.B) {
	dir := b.TempDir()
	s := Open(filepath.Join(dir, "bench.db"))
	if s == nil {
		b.Fatal("Open returned nil")
	}
	defer s.Close()

	data := []byte(`{"issues": [{"key": "SR-123", "summary": "test issue"}]}`)
	key := JiraUserKey("bench@example.com")

	// Pre-populate
	s.Put(key, "jira", data, time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := s.Get(key)
		if err != nil {
			b.Fatal(err)
		}
		if got == nil {
			b.Fatal("unexpected nil")
		}
	}
}

func TestCorruptionRecovery(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "corrupt.db")

	// Write garbage to the file.
	os.WriteFile(dbPath, []byte("not a database"), 0o600)

	// Open should return nil (graceful degradation).
	s := Open(dbPath)
	if s != nil {
		s.Close()
		t.Fatal("expected nil Store for corrupted database")
	}
}

// tempEncryptedDB opens an encrypted Store backed by a temp DB and config dir.
func tempEncryptedDB(t *testing.T, passphrase string) *Store {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, "config")
	s := OpenWithPassphrase(filepath.Join(dir, "test.db"), cfgDir, passphrase)
	if s == nil {
		t.Fatal("OpenWithPassphrase returned nil")
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// TestOpenWithPassphrase_PutGet verifies a full encrypted Put/Get roundtrip.
func TestOpenWithPassphrase_PutGet(t *testing.T) {
	s := tempEncryptedDB(t, "correct-horse")
	data := []byte(`{"jira":"issue"}`)

	if err := s.Put("k1", "jira", data, time.Hour); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get = %q, want %q", got, data)
	}
}

// TestEncryptedEntryWithoutPassphrase verifies that an encrypted entry read by
// a plain (non-passphrase) Store returns nil (cache miss).
func TestEncryptedEntryWithoutPassphrase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgDir := filepath.Join(dir, "config")

	// Write encrypted entry.
	sEnc := OpenWithPassphrase(dbPath, cfgDir, "secret")
	if sEnc == nil {
		t.Fatal("OpenWithPassphrase returned nil")
	}
	if err := sEnc.Put("k1", "jira", []byte(`"data"`), time.Hour); err != nil {
		sEnc.Close()
		t.Fatalf("Put: %v", err)
	}
	sEnc.Close()

	// Read with plain store — should see cache miss.
	sPlain := Open(dbPath)
	if sPlain == nil {
		t.Fatal("Open returned nil")
	}
	defer sPlain.Close()

	got, err := sPlain.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != nil {
		t.Errorf("Get(encrypted entry, no passphrase) = %q, want nil", got)
	}
}

// TestLegacyPlaintextWithPassphrase verifies that a plaintext (legacy) entry
// stored without encryption is still readable by a passphrase-enabled Store.
func TestLegacyPlaintextWithPassphrase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgDir := filepath.Join(dir, "config")
	data := []byte(`{"legacy":"entry"}`)

	// Write plaintext entry via plain store.
	sPlain := Open(dbPath)
	if sPlain == nil {
		t.Fatal("Open returned nil")
	}
	if err := sPlain.Put("k1", "jira", data, time.Hour); err != nil {
		sPlain.Close()
		t.Fatalf("Put: %v", err)
	}
	sPlain.Close()

	// Read with encrypted store — backward compat.
	sEnc := OpenWithPassphrase(dbPath, cfgDir, "secret")
	if sEnc == nil {
		t.Fatal("OpenWithPassphrase returned nil")
	}
	defer sEnc.Close()

	got, err := sEnc.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get(legacy plaintext) = %q, want %q", got, data)
	}
}

// TestOpenWithPassphrase_WrongPassphrase verifies graceful degradation when the
// passphrase is wrong for an existing key file.
func TestOpenWithPassphrase_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	cfgDir := filepath.Join(dir, "config")

	// Create key file with correct passphrase.
	s := OpenWithPassphrase(dbPath, cfgDir, "right-pass")
	if s == nil {
		t.Fatal("OpenWithPassphrase returned nil on first open")
	}
	s.Close()

	// Re-open with wrong passphrase — should return nil.
	s2 := OpenWithPassphrase(dbPath, cfgDir, "wrong-pass")
	if s2 != nil {
		s2.Close()
		t.Fatal("OpenWithPassphrase with wrong passphrase should return nil")
	}
}

// TestUpsert_Encrypted verifies that a second Put overwrites the first and
// Get returns the second value.
func TestUpsert_Encrypted(t *testing.T) {
	s := tempEncryptedDB(t, "upsert-pass")

	if err := s.Put("k1", "test", []byte(`"v1"`), time.Hour); err != nil {
		t.Fatalf("Put v1: %v", err)
	}
	if err := s.Put("k1", "test", []byte(`"v2"`), time.Hour); err != nil {
		t.Fatalf("Put v2: %v", err)
	}
	got, err := s.Get("k1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != `"v2"` {
		t.Errorf("Get after upsert = %q, want %q", got, `"v2"`)
	}
}
