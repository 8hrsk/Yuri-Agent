package backup

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRestoreRejectsZeroLengthKDFSalt covers N-7.
//
// KDFParams.Validate deliberately accepts an empty salt, because the export
// path validates parameters before generating one. The restore path has no
// generation step, so before the fix an envelope header carrying "salt": ""
// passed validation and deriveKey ran scrypt with no salt at all: the same
// passphrase then produced the same key for every such archive, defeating
// per-archive key separation.
//
// The crafted archive below is a genuine, correctly authenticated envelope -
// it is re-sealed with the very key that the empty salt derives - so it would
// decrypt and restore cleanly if the guard were removed. That is what makes
// this a real negative control rather than a malformed-input test.
//
// Since H-15 the helpers here span both envelope shapes: Export now writes the
// chunked version 2 envelope, so openEnvelopeForTest dispatches on the header
// version, while the crafted archive is deliberately built as a version 1
// single seal. That keeps the salt guard under test on the legacy path too, and
// proves the legacy path is still reachable rather than dead code.
func TestRestoreRejectsZeroLengthKDFSalt(t *testing.T) {
	ctx := context.Background()
	const passphrase = "correct horse battery staple"

	database, _ := testDatabase(t)
	defer database.Close()
	if _, err := database.Exec("CREATE TABLE notes (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec("INSERT INTO notes(body) VALUES ('salt separation')"); err != nil {
		t.Fatal(err)
	}

	archivePath := filepath.Join(t.TempDir(), "backup.ybk")
	if _, err := Export(ctx, database, archivePath, passphrase, ExportOptions{}); err != nil {
		t.Fatalf("Export() error = %v", err)
	}

	// The export path must keep working: it is the generate-on-empty caller
	// that requires Validate to tolerate an empty salt in the first place.
	manifest, err := Validate(ctx, archivePath, passphrase, RestoreOptions{})
	if err != nil {
		t.Fatalf("Validate() on the exported archive error = %v", err)
	}
	if manifest.Database.Path != "database.sqlite3" {
		t.Fatalf("manifest = %#v", manifest)
	}

	original, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	header, payload := openEnvelopeForTest(t, original, passphrase)
	if header.Version != envelopeVersionChunked || header.Framing != framingSTREAM {
		t.Fatalf("Export() wrote envelope version %d framing %q, want the chunked format", header.Version, header.Framing)
	}
	if header.Salt == "" {
		t.Fatal("the exported archive must carry a generated salt")
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(header.Salt); err != nil || len(decoded) != 32 {
		t.Fatalf("exported salt = %q (decoded %d bytes, err %v), want 32 bytes", header.Salt, len(decoded), err)
	}

	// The old rule still accepts an empty salt - that escape is what the
	// restore path must no longer inherit.
	empty := KDFParams{N: header.N, R: header.R, P: header.P}
	if err := empty.Validate(); err != nil {
		t.Fatalf("Validate() with an empty salt = %v, want nil (the export path relies on this)", err)
	}
	if err := empty.ValidateForRestore(); !errors.Is(err, ErrInvalidArchive) {
		t.Fatalf("ValidateForRestore() with an empty salt = %v, want ErrInvalidArchive", err)
	}

	craftedPath := filepath.Join(t.TempDir(), "zero-salt.ybk")
	crafted := sealEnvelopeWithEmptySaltForTest(t, header, payload, passphrase)
	if err := os.WriteFile(craftedPath, crafted, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, run := range map[string]func() error{
		"Validate": func() error {
			_, err := Validate(ctx, craftedPath, passphrase, RestoreOptions{})
			return err
		},
		"Restore": func() error {
			_, err := Restore(ctx, craftedPath, passphrase, RestoreOptions{TargetDir: filepath.Join(t.TempDir(), "restored")})
			return err
		},
	} {
		err := run()
		if !errors.Is(err, ErrInvalidArchive) {
			t.Fatalf("%s() on a zero-salt archive = %v, want ErrInvalidArchive", name, err)
		}
		if !strings.Contains(err.Error(), "salt") {
			t.Fatalf("%s() error = %v, want the salt guard to be named", name, err)
		}
		// A GCM failure would mean the archive was merely undecryptable. This
		// one authenticates correctly; it must be refused by the salt guard
		// before any key is derived.
		if errors.Is(err, ErrWrongPassphrase) {
			t.Fatalf("%s() rejected the archive at GCM, not at the salt guard: %v", name, err)
		}
	}

	// The crafted envelope really is decryptable with the empty salt, so the
	// only thing standing between it and a successful restore is the guard.
	if key, err := deriveKey(ctx, passphrase, KDFParams{N: header.N, R: header.R, P: header.P}); err != nil {
		t.Fatalf("deriveKey with an empty salt = %v", err)
	} else if plaintext := openCraftedForTest(t, crafted, key); !bytes.Equal(plaintext, payload) {
		t.Fatal("crafted zero-salt envelope did not round-trip; the test fixture is wrong")
	}
}

// openEnvelopeForTest parses an archive and returns its header plus the
// decrypted payload, for either envelope shape.
func openEnvelopeForTest(t *testing.T, content []byte, passphrase string) (envelopeHeader, []byte) {
	t.Helper()
	header, headerBytes, headerEnd := splitEnvelopeForTest(t, content)
	salt, err := base64.RawStdEncoding.DecodeString(header.Salt)
	if err != nil {
		t.Fatal(err)
	}
	nonce, err := base64.RawStdEncoding.DecodeString(header.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	key, err := deriveKey(context.Background(), passphrase, KDFParams{N: header.N, R: header.R, P: header.P, Salt: salt})
	if err != nil {
		t.Fatal(err)
	}
	gcm := gcmForTest(t, key)
	associated := append(append([]byte(nil), envelopeMagic[:]...), headerBytes...)
	if header.Version == envelopeVersionChunked {
		var payload bytes.Buffer
		if _, err := openChunkStream(context.Background(), bytes.NewReader(content[headerEnd:]), &payload, gcm, nonce, associated, header.ChunkSize, DefaultMaxPlaintextBytes); err != nil {
			t.Fatal(err)
		}
		return header, payload.Bytes()
	}
	payload, err := gcm.Open(nil, nonce, content[headerEnd:], associated)
	if err != nil {
		t.Fatal(err)
	}
	return header, payload
}

// splitEnvelopeForTest returns the parsed header, its exact bytes (the AEAD
// associated data), and the offset at which the body starts.
func splitEnvelopeForTest(t *testing.T, content []byte) (envelopeHeader, []byte, int) {
	t.Helper()
	headerStart := len(envelopeMagic) + 4
	headerSize := int(binary.BigEndian.Uint32(content[len(envelopeMagic):headerStart]))
	headerEnd := headerStart + headerSize
	headerBytes := content[headerStart:headerEnd]

	var header envelopeHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		t.Fatal(err)
	}
	return header, headerBytes, headerEnd
}

// sealEnvelopeWithEmptySaltForTest rebuilds a valid version 1 envelope around
// payload whose header declares an empty salt and whose key is derived with
// that empty salt. The KDF parameters match the genuine archive.
func sealEnvelopeWithEmptySaltForTest(t *testing.T, template envelopeHeader, payload []byte, passphrase string) []byte {
	t.Helper()
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		t.Fatal(err)
	}
	header := template
	header.Version = envelopeVersionSealed
	header.Framing = ""
	header.ChunkSize = 0
	header.Salt = ""
	header.Nonce = base64.RawStdEncoding.EncodeToString(nonce)
	header.PlaintextSize = int64(len(payload))
	header.CiphertextSize = int64(len(payload) + 16)

	headerBytes, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(headerBytes, []byte(`"salt":""`)) {
		t.Fatalf("crafted header does not carry an empty salt: %s", headerBytes)
	}
	key, err := deriveKey(context.Background(), passphrase, KDFParams{N: header.N, R: header.R, P: header.P})
	if err != nil {
		t.Fatal(err)
	}
	gcm := gcmForTest(t, key)
	associated := append(append([]byte(nil), envelopeMagic[:]...), headerBytes...)
	ciphertext := gcm.Seal(nil, nonce, payload, associated)

	var output bytes.Buffer
	output.Write(envelopeMagic[:])
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(headerBytes)))
	output.Write(size[:])
	output.Write(headerBytes)
	output.Write(ciphertext)
	return output.Bytes()
}

// openCraftedForTest decrypts a crafted envelope with an already-derived key.
func openCraftedForTest(t *testing.T, content, key []byte) []byte {
	t.Helper()
	header, headerBytes, headerEnd := splitEnvelopeForTest(t, content)
	nonce, err := base64.RawStdEncoding.DecodeString(header.Nonce)
	if err != nil {
		t.Fatal(err)
	}
	gcm := gcmForTest(t, key)
	associated := append(append([]byte(nil), envelopeMagic[:]...), headerBytes...)
	plaintext, err := gcm.Open(nil, nonce, content[headerEnd:], associated)
	if err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func gcmForTest(t *testing.T, key []byte) cipher.AEAD {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return gcm
}
