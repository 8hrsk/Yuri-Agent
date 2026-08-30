package backup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

const streamPassphrase = "correct horse battery staple"

// TestStreamedRoundTripMatchesSourceBytes exports a database and several blobs
// large enough to span many frames, restores them, and compares every restored
// file with its source byte for byte.
func TestStreamedRoundTripMatchesSourceBytes(t *testing.T) {
	ctx := context.Background()
	database, _ := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	// Comfortably more than one 1 MiB frame, so the round trip exercises the
	// non-final frame path rather than a single sealed chunk.
	body := randomBytes(t, 512<<10)
	for i := 0; i < 8; i++ {
		if _, err := database.Exec("INSERT INTO payload(body) VALUES (?)", body); err != nil {
			t.Fatal(err)
		}
	}

	blobDirectory := t.TempDir()
	sources := map[string][]byte{
		"small.bin":        randomBytes(t, 17),
		"exact-frame.bin":  randomBytes(t, defaultChunkPlaintextBytes),
		"multi-frame.bin":  randomBytes(t, 3*defaultChunkPlaintextBytes+7),
		"nested/inner.bin": randomBytes(t, 1024),
	}
	for name, content := range sources {
		path := filepath.Join(blobDirectory, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	archive := filepath.Join(t.TempDir(), "backup.ybk")
	manifest, err := Export(ctx, database, archive, streamPassphrase, ExportOptions{
		ConfigMetadata: json.RawMessage(`{"version":1,"locale":"ru-RU"}`),
		BlobDirectory:  blobDirectory,
		Limits:         Limits{MaxBlobBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	if len(manifest.Blobs) != len(sources) {
		t.Fatalf("manifest blobs = %d, want %d", len(manifest.Blobs), len(sources))
	}

	// The exported snapshot is a VACUUM INTO image, not a copy of the live
	// file, so the database is compared against the archive's own manifest
	// digest rather than against the active database on disk.
	target := filepath.Join(t.TempDir(), "restore")
	result, err := Restore(ctx, archive, streamPassphrase, RestoreOptions{
		TargetDir: target,
		Limits:    Limits{MaxBlobBytes: 8 << 20},
	})
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	restoredDatabase, err := os.ReadFile(result.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(restoredDatabase)) != manifest.Database.Size || digestBytes(restoredDatabase) != manifest.Database.SHA256 {
		t.Fatalf("restored database = %d bytes / %s, want %d / %s",
			len(restoredDatabase), digestBytes(restoredDatabase), manifest.Database.Size, manifest.Database.SHA256)
	}
	if len(result.BlobPaths) != len(sources) {
		t.Fatalf("restored blob paths = %d, want %d", len(result.BlobPaths), len(sources))
	}
	for name, expected := range sources {
		path := filepath.Join(target, "blobs", filepath.FromSlash(name))
		restored, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatalf("read restored blob %q: %v", name, readErr)
		}
		if !bytes.Equal(restored, expected) {
			t.Fatalf("restored blob %q differs from its source (%d vs %d bytes)", name, len(restored), len(expected))
		}
	}
}

// TestLegacySealedArchiveStillRestores restores testdata/legacy-v1.ybk, which
// was produced by the pre-H-15 single-seal exporter and checked in unchanged.
// If the framed format ever orphans existing backups, this fails.
func TestLegacySealedArchiveStillRestores(t *testing.T) {
	ctx := context.Background()
	archive, err := filepath.Abs(filepath.Join("testdata", "legacy-v1.ybk"))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	header, _, _ := splitEnvelopeForTest(t, content)
	if header.Version != envelopeVersionSealed {
		t.Fatalf("fixture envelope version = %d, want the legacy single seal", header.Version)
	}
	if header.Framing != "" || header.ChunkSize != 0 {
		t.Fatalf("fixture declares framing %q/%d, so it is not a legacy archive", header.Framing, header.ChunkSize)
	}

	expected, err := os.ReadFile(filepath.Join("testdata", "legacy-v1.manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var recorded Manifest
	if err := json.Unmarshal(expected, &recorded); err != nil {
		t.Fatal(err)
	}

	validated, err := Validate(ctx, archive, legacyFixturePassphrase, RestoreOptions{})
	if err != nil {
		t.Fatalf("Validate() on the legacy fixture error = %v", err)
	}
	if validated.Database != recorded.Database || len(validated.Blobs) != len(recorded.Blobs) {
		t.Fatalf("validated manifest = %#v, want %#v", validated, recorded)
	}

	target := filepath.Join(t.TempDir(), "restore")
	result, err := Restore(ctx, archive, legacyFixturePassphrase, RestoreOptions{TargetDir: target})
	if err != nil {
		t.Fatalf("Restore() on the legacy fixture error = %v", err)
	}
	restored, err := os.ReadFile(result.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	if digestBytes(restored) != recorded.Database.SHA256 {
		t.Fatalf("restored legacy database digest = %s, want %s", digestBytes(restored), recorded.Database.SHA256)
	}
	if len(result.BlobPaths) != len(recorded.Blobs) {
		t.Fatalf("restored %d legacy blobs, want %d", len(result.BlobPaths), len(recorded.Blobs))
	}
	for index, entry := range recorded.Blobs {
		blob, readErr := os.ReadFile(result.BlobPaths[index])
		if readErr != nil {
			t.Fatal(readErr)
		}
		if digestBytes(blob) != entry.SHA256 {
			t.Fatalf("restored legacy blob %q digest = %s, want %s", entry.Path, digestBytes(blob), entry.SHA256)
		}
	}
	if result.ConfigPath == "" || len(result.ConfigMetadata) == 0 {
		t.Fatalf("legacy config metadata was not restored: %#v", result)
	}

	// The fixture must never be silently regenerated by a later change.
	digest := sha256.Sum256(content)
	const fixtureDigest = "7be76ab6c9f441a707bd13bb343f6be83d97a00dede3b50e0717e126df0f31a6"
	if hex.EncodeToString(digest[:]) != fixtureDigest {
		t.Fatalf("legacy fixture digest = %s, want %s (the fixture must stay byte-identical)", hex.EncodeToString(digest[:]), fixtureDigest)
	}
}

// TestFramedArchiveRejectsTruncation removes the final frame, which is the
// attack the last-frame marker exists to stop: without it the truncated stream
// would authenticate as a complete, shorter archive.
func TestFramedArchiveRejectsTruncation(t *testing.T) {
	ctx := context.Background()
	archive, frames := framedArchiveForTest(t, 3)

	for _, cut := range []struct {
		name  string
		bytes int
	}{
		{"final frame removed", len(frames[len(frames)-1])},
		{"one byte removed", 1},
	} {
		truncated := filepath.Join(t.TempDir(), "truncated.ybk")
		content, err := os.ReadFile(archive)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(truncated, content[:len(content)-cut.bytes], 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = Validate(ctx, truncated, streamPassphrase, RestoreOptions{})
		if err == nil {
			t.Fatalf("%s: Validate() accepted a truncated archive", cut.name)
		}
		// ErrWrongPassphrase specifically, not merely "some error": it is the
		// AEAD that must reject this. Removing the final frame leaves a body
		// whose last surviving frame was sealed as non-final, so opening it as
		// final fails. Accepting ErrInvalidArchive here would let the test pass
		// on the zip parser noticing a short payload, which would still hold if
		// the final-frame marker were dropped.
		if !errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("%s: Validate() error = %v, want ErrWrongPassphrase from the frame authentication", cut.name, err)
		}
	}
}

// TestFramedArchiveRejectsReorderedFrames swaps two full frames. Each frame is
// individually well formed and sealed under the right key; only its index has
// changed, so this fails exactly when the counter is part of the nonce.
func TestFramedArchiveRejectsReorderedFrames(t *testing.T) {
	ctx := context.Background()
	archive, frames := framedArchiveForTest(t, 4)
	if len(frames) < 3 {
		t.Fatalf("archive has %d frames, need at least 3 to reorder", len(frames))
	}
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	_, _, bodyStart := splitEnvelopeForTest(t, content)

	swapped := append([]byte(nil), content[:bodyStart]...)
	reordered := append([][]byte(nil), frames...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	for _, frame := range reordered {
		swapped = append(swapped, frame...)
	}
	if len(swapped) != len(content) {
		t.Fatalf("reordered archive is %d bytes, want %d", len(swapped), len(content))
	}
	if bytes.Equal(swapped, content) {
		t.Fatal("reordering produced an identical archive; the fixture is wrong")
	}
	path := filepath.Join(t.TempDir(), "reordered.ybk")
	if err := os.WriteFile(path, swapped, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(ctx, path, streamPassphrase, RestoreOptions{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Validate() on reordered frames = %v, want ErrWrongPassphrase", err)
	}
}

// TestFramedArchiveRejectsCrossArchiveSplice replaces a frame with the frame at
// the same index from a second archive.
//
// Both archives are exported with the same passphrase AND the same KDF salt, so
// they are sealed under the identical key. That is what makes this a real
// splice test: key separation cannot be doing the work, and the only things
// standing in the way are the per-archive nonce prefix and the associated data,
// which covers the header.
func TestFramedArchiveRejectsCrossArchiveSplice(t *testing.T) {
	ctx := context.Background()
	sharedSalt := randomBytes(t, 32)
	first, firstFrames := framedArchiveWithSaltForTest(t, 4, sharedSalt)
	second, secondFrames := framedArchiveWithSaltForTest(t, 4, sharedSalt)
	if len(firstFrames) < 2 || len(secondFrames) < 2 {
		t.Fatalf("archives have %d/%d frames, need at least 2", len(firstFrames), len(secondFrames))
	}
	if bytes.Equal(firstFrames[0], secondFrames[0]) {
		t.Fatal("the two archives share a frame; the fixture is wrong")
	}
	content, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	firstHeader, _, bodyStart := splitEnvelopeForTest(t, content)
	secondContent, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	secondHeader, _, _ := splitEnvelopeForTest(t, secondContent)
	if firstHeader.Salt != secondHeader.Salt {
		t.Fatalf("archives were sealed under different salts (%q vs %q); the splice would be blocked by key separation alone", firstHeader.Salt, secondHeader.Salt)
	}
	if firstHeader.Nonce == secondHeader.Nonce {
		t.Fatal("archives share a nonce; the fixture is wrong")
	}
	spliced := append([]byte(nil), content[:bodyStart]...)
	spliced = append(spliced, secondFrames[0]...)
	for _, frame := range firstFrames[1:] {
		spliced = append(spliced, frame...)
	}
	path := filepath.Join(t.TempDir(), "spliced.ybk")
	if err := os.WriteFile(path, spliced, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Validate(ctx, path, streamPassphrase, RestoreOptions{}); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("Validate() on a cross-archive splice = %v, want ErrWrongPassphrase", err)
	}
}

// TestFramedEnvelopeRejectsMismatchedShape covers the version dispatch: a
// framed header must not be honoured without its framing marker, and a legacy
// header must not claim one.
func TestFramedEnvelopeRejectsMismatchedShape(t *testing.T) {
	limits := DefaultLimits()
	for name, header := range map[string]envelopeHeader{
		"framed without framing marker": {Version: envelopeVersionChunked, ChunkSize: defaultChunkPlaintextBytes},
		"framed with foreign framing":   {Version: envelopeVersionChunked, Framing: "aes-gcm-siv", ChunkSize: defaultChunkPlaintextBytes},
		"framed with tiny chunks":       {Version: envelopeVersionChunked, Framing: framingSTREAM, ChunkSize: 8},
		"framed with huge chunks":       {Version: envelopeVersionChunked, Framing: framingSTREAM, ChunkSize: maxChunkPlaintextBytes + 1},
		"framed declaring sizes":        {Version: envelopeVersionChunked, Framing: framingSTREAM, ChunkSize: defaultChunkPlaintextBytes, PlaintextSize: 1, CiphertextSize: 17},
		"sealed declaring framing":      {Version: envelopeVersionSealed, Framing: framingSTREAM, ChunkSize: defaultChunkPlaintextBytes},
		"unknown version":               {Version: 7, Framing: framingSTREAM, ChunkSize: defaultChunkPlaintextBytes},
	} {
		if err := validateEnvelopeFraming(header, 1024, limits); !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("%s: validateEnvelopeFraming() = %v, want ErrInvalidArchive", name, err)
		}
	}
}

// TestDecodeLeavesNoTemporaryFiles pins requirement 4 of the H-15 fix: the
// staged plaintext must not outlive the call on any path. TMPDIR is redirected
// at a fresh directory so the assertion is exact rather than a heuristic scan of
// the real temp root.
func TestDecodeLeavesNoTemporaryFiles(t *testing.T) {
	temporaryRoot := t.TempDir()
	t.Setenv("TMPDIR", temporaryRoot)
	if os.TempDir() != temporaryRoot {
		t.Skipf("os.TempDir() = %q, not the redirected %q", os.TempDir(), temporaryRoot)
	}

	database, _ := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO notes(body) VALUES ('temporary hygiene')"); err != nil {
		t.Fatal(err)
	}
	blobDirectory := t.TempDir()
	if err := os.WriteFile(filepath.Join(blobDirectory, "note.bin"), randomBytes(t, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "backup.ybk")
	if _, err := Export(context.Background(), database, archive, streamPassphrase, ExportOptions{BlobDirectory: blobDirectory}); err != nil {
		t.Fatal(err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, run := range map[string]func() error{
		"validate": func() error {
			_, err := Validate(context.Background(), archive, streamPassphrase, RestoreOptions{})
			return err
		},
		"restore": func() error {
			_, err := Restore(context.Background(), archive, streamPassphrase, RestoreOptions{TargetDir: filepath.Join(t.TempDir(), "out")})
			return err
		},
		"wrong passphrase": func() error {
			_, err := Validate(context.Background(), archive, "not the passphrase", RestoreOptions{})
			if !errors.Is(err, ErrWrongPassphrase) {
				t.Fatalf("wrong passphrase error = %v", err)
			}
			return nil
		},
		"cancelled": func() error {
			_, err := Validate(cancelled, archive, streamPassphrase, RestoreOptions{})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancelled Validate() error = %v", err)
			}
			return nil
		},
	} {
		if err := run(); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		leftovers, err := os.ReadDir(temporaryRoot)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range leftovers {
			names = append(names, entry.Name())
		}
		if len(names) != 0 {
			t.Fatalf("%s left temporary files behind: %v", name, names)
		}
	}
}

// framedArchiveForTest exports an archive whose payload spans at least
// minimumFrames frames and returns its path plus the exact frame boundaries of
// its body.
func framedArchiveForTest(t *testing.T, minimumFrames int) (string, [][]byte) {
	t.Helper()
	return framedArchiveWithSaltForTest(t, minimumFrames, nil)
}

// framedArchiveWithSaltForTest pins the KDF salt so two archives can be built
// under one key.
func framedArchiveWithSaltForTest(t *testing.T, minimumFrames int, salt []byte) (string, [][]byte) {
	t.Helper()
	database, _ := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE payload (id INTEGER PRIMARY KEY, body BLOB NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < minimumFrames; i++ {
		if _, err := database.Exec("INSERT INTO payload(body) VALUES (?)", randomBytes(t, defaultChunkPlaintextBytes)); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "framed.ybk")
	if _, err := Export(context.Background(), database, archive, streamPassphrase, ExportOptions{KDF: KDFParams{Salt: salt}}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}
	content, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	header, _, bodyStart := splitEnvelopeForTest(t, content)
	if header.Version != envelopeVersionChunked {
		t.Fatalf("Export() wrote envelope version %d, want the framed format", header.Version)
	}
	frameSize := header.ChunkSize + gcmTagBytes
	body := content[bodyStart:]
	var frames [][]byte
	for len(body) > frameSize {
		frames = append(frames, body[:frameSize])
		body = body[frameSize:]
	}
	frames = append(frames, body)
	if len(frames) < minimumFrames {
		t.Fatalf("archive has %d frames, want at least %d", len(frames), minimumFrames)
	}
	return archive, frames
}

func randomBytes(t *testing.T, size int) []byte {
	t.Helper()
	buffer := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, buffer); err != nil {
		t.Fatal(err)
	}
	return buffer
}

// legacyFixturePassphrase is the passphrase the checked-in legacy archive was
// exported with. It protects a synthetic test database and nothing else.
const legacyFixturePassphrase = "legacy fixture passphrase"
