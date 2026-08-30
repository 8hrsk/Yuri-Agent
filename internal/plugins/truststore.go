package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Trust states a package can be in. They are stored verbatim in the plugin
// record so the UI and the supervisor agree on one vocabulary.
const (
	// TrustUnsigned means the manifest carries no signature at all.
	TrustUnsigned = "unsigned"
	// TrustUnverified means a signature is present but the host could not
	// tie it to a publisher key the owner explicitly trusted.
	TrustUnverified = "unverified"
	// TrustVerified means an ed25519 signature over the canonical manifest
	// was verified against a key in the local trust store, and the manifest
	// pins the executable digest.
	TrustVerified = "verified"
)

const (
	// TrustStoreFileName is the on-disk name of the publisher key store.
	TrustStoreFileName = "publishers.json"
	// trustStoreVersion guards against silently reading a future format.
	trustStoreVersion = 1
	// signingDomain separates plugin manifest signatures from any other
	// ed25519 signature the same key might ever produce.
	signingDomain = "yuri-plugin-manifest/v1\n"
	// maxTrustStoreBytes bounds the store file read.
	maxTrustStoreBytes = 1 << 20
	// maxTrustedKeys bounds how many publishers the owner can trust.
	maxTrustedKeys = 256
)

var (
	// ErrTrustStoreCorrupt is returned when the store file cannot be parsed.
	ErrTrustStoreCorrupt = errors.New("plugin: publisher trust store is corrupt")
	// ErrPublisherKeyInvalid is returned for malformed key material.
	ErrPublisherKeyInvalid = errors.New("plugin: publisher key is invalid")
	// ErrPublisherKeyExists is returned when a key id is already trusted
	// with different material. Rotation must go through an explicit revoke.
	ErrPublisherKeyExists = errors.New("plugin: publisher key id is already trusted")
)

// PublisherKey is one ed25519 public key the owner has explicitly decided to
// trust. A key never arrives with a package: it is added by a separate,
// audited owner action.
type PublisherKey struct {
	KeyID     string    `json:"key_id"`
	Algorithm string    `json:"algorithm"`
	PublicKey string    `json:"public_key"`
	Publisher string    `json:"publisher,omitempty"`
	Comment   string    `json:"comment,omitempty"`
	AddedAt   time.Time `json:"added_at"`
}

type trustStoreDocument struct {
	Version    int            `json:"version"`
	Publishers []PublisherKey `json:"publishers"`
}

// TrustStore is a small local file of owner-approved publisher keys. It is
// deliberately not a PKI: there is no chain, no revocation list and no
// network. It answers exactly one question — did the owner previously and
// explicitly say that this key id speaks for this publisher.
type TrustStore struct {
	path string
	mu   sync.Mutex
}

// OpenTrustStore binds a store to a path without touching the filesystem.
func OpenTrustStore(path string) *TrustStore {
	return &TrustStore{path: strings.TrimSpace(path)}
}

// Path returns the backing file path.
func (store *TrustStore) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

// Keys returns the trusted publisher keys sorted by key id. A missing file is
// an empty store, not an error: a fresh installation trusts nobody.
func (store *TrustStore) Keys() ([]PublisherKey, error) {
	if store == nil || store.path == "" {
		return nil, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.read()
	if err != nil {
		return nil, err
	}
	return document.Publishers, nil
}

// Add records one publisher key. It refuses to silently replace an existing
// key id so that a compromised or stale key must be revoked deliberately.
func (store *TrustStore) Add(key PublisherKey) (PublisherKey, error) {
	if store == nil || store.path == "" {
		return PublisherKey{}, errors.New("plugin: trust store path is not configured")
	}
	normalized, err := normalizePublisherKey(key)
	if err != nil {
		return PublisherKey{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.read()
	if err != nil {
		return PublisherKey{}, err
	}
	for _, existing := range document.Publishers {
		if existing.KeyID != normalized.KeyID {
			continue
		}
		if existing.PublicKey == normalized.PublicKey &&
			strings.EqualFold(existing.Publisher, normalized.Publisher) {
			return existing, nil
		}
		return PublisherKey{}, fmt.Errorf("%w: %s", ErrPublisherKeyExists, normalized.KeyID)
	}
	if len(document.Publishers) >= maxTrustedKeys {
		return PublisherKey{}, fmt.Errorf("%w: trust store holds the maximum of %d keys", ErrPublisherKeyInvalid, maxTrustedKeys)
	}
	document.Publishers = append(document.Publishers, normalized)
	if err := store.write(document); err != nil {
		return PublisherKey{}, err
	}
	return normalized, nil
}

// Remove deletes one key id and reports whether it was present.
func (store *TrustStore) Remove(keyID string) (bool, error) {
	if store == nil || store.path == "" {
		return false, errors.New("plugin: trust store path is not configured")
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return false, fmt.Errorf("%w: key_id is required", ErrPublisherKeyInvalid)
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	document, err := store.read()
	if err != nil {
		return false, err
	}
	remaining := make([]PublisherKey, 0, len(document.Publishers))
	removed := false
	for _, existing := range document.Publishers {
		if existing.KeyID == keyID {
			removed = true
			continue
		}
		remaining = append(remaining, existing)
	}
	if !removed {
		return false, nil
	}
	document.Publishers = remaining
	if err := store.write(document); err != nil {
		return false, err
	}
	return true, nil
}

func (store *TrustStore) read() (trustStoreDocument, error) {
	file, err := os.Open(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return trustStoreDocument{Version: trustStoreVersion, Publishers: []PublisherKey{}}, nil
	}
	if err != nil {
		return trustStoreDocument{}, fmt.Errorf("open publisher trust store: %w", err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxTrustStoreBytes+1))
	if err != nil {
		return trustStoreDocument{}, fmt.Errorf("read publisher trust store: %w", err)
	}
	if len(content) > maxTrustStoreBytes {
		return trustStoreDocument{}, fmt.Errorf("%w: file exceeds %d bytes", ErrTrustStoreCorrupt, maxTrustStoreBytes)
	}
	var document trustStoreDocument
	if err := json.Unmarshal(content, &document); err != nil {
		return trustStoreDocument{}, fmt.Errorf("%w: %v", ErrTrustStoreCorrupt, err)
	}
	if document.Version != trustStoreVersion {
		return trustStoreDocument{}, fmt.Errorf("%w: unsupported version %d", ErrTrustStoreCorrupt, document.Version)
	}
	normalized := make([]PublisherKey, 0, len(document.Publishers))
	seen := make(map[string]struct{}, len(document.Publishers))
	for _, key := range document.Publishers {
		candidate, err := normalizePublisherKey(key)
		if err != nil {
			return trustStoreDocument{}, fmt.Errorf("%w: %v", ErrTrustStoreCorrupt, err)
		}
		if _, duplicate := seen[candidate.KeyID]; duplicate {
			return trustStoreDocument{}, fmt.Errorf("%w: duplicate key id %q", ErrTrustStoreCorrupt, candidate.KeyID)
		}
		seen[candidate.KeyID] = struct{}{}
		normalized = append(normalized, candidate)
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].KeyID < normalized[j].KeyID })
	document.Publishers = normalized
	return document, nil
}

func (store *TrustStore) write(document trustStoreDocument) error {
	document.Version = trustStoreVersion
	sort.Slice(document.Publishers, func(i, j int) bool {
		return document.Publishers[i].KeyID < document.Publishers[j].KeyID
	})
	content, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return err
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create trust store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".publishers-*.tmp")
	if err != nil {
		return fmt.Errorf("stage trust store: %w", err)
	}
	staged := temporary.Name()
	defer os.Remove(staged)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("protect trust store: %w", err)
	}
	if _, err := temporary.Write(append(content, '\n')); err != nil {
		temporary.Close()
		return fmt.Errorf("write trust store: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync trust store: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close trust store: %w", err)
	}
	if err := os.Rename(staged, store.path); err != nil {
		return fmt.Errorf("commit trust store: %w", err)
	}
	return nil
}

func normalizePublisherKey(key PublisherKey) (PublisherKey, error) {
	key.KeyID = strings.TrimSpace(key.KeyID)
	key.Algorithm = strings.ToLower(strings.TrimSpace(key.Algorithm))
	key.PublicKey = strings.TrimSpace(key.PublicKey)
	key.Publisher = strings.TrimSpace(key.Publisher)
	key.Comment = strings.TrimSpace(key.Comment)
	if key.KeyID == "" || len(key.KeyID) > 128 {
		return PublisherKey{}, fmt.Errorf("%w: key_id is required and must be at most 128 characters", ErrPublisherKeyInvalid)
	}
	if key.Algorithm == "" {
		key.Algorithm = "ed25519"
	}
	if key.Algorithm != "ed25519" {
		return PublisherKey{}, fmt.Errorf("%w: unsupported algorithm %q", ErrPublisherKeyInvalid, key.Algorithm)
	}
	if len(key.Publisher) > 256 || len(key.Comment) > 512 {
		return PublisherKey{}, fmt.Errorf("%w: publisher or comment is too long", ErrPublisherKeyInvalid)
	}
	material, err := decodeKeyMaterial(key.PublicKey)
	if err != nil {
		return PublisherKey{}, err
	}
	if len(material) != ed25519.PublicKeySize {
		return PublisherKey{}, fmt.Errorf("%w: ed25519 public key must be %d bytes", ErrPublisherKeyInvalid, ed25519.PublicKeySize)
	}
	// Re-encode canonically so the same key can never be stored twice under
	// two spellings of the same bytes.
	key.PublicKey = base64.StdEncoding.EncodeToString(material)
	if key.AddedAt.IsZero() {
		key.AddedAt = time.Now().UTC()
	}
	key.AddedAt = key.AddedAt.UTC()
	return key, nil
}

func decodeKeyMaterial(value string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("%w: public_key is required", ErrPublisherKeyInvalid)
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(value); err == nil {
		return decoded, nil
	}
	return nil, fmt.Errorf("%w: public_key must be base64 or hex", ErrPublisherKeyInvalid)
}

// TrustDecision is the outcome of checking one package against the store.
type TrustDecision struct {
	Status    string
	KeyID     string
	Publisher string
	Reason    string
}

// Verified reports whether the package may run outside developer mode.
func (decision TrustDecision) Verified() bool { return decision.Status == TrustVerified }

// VerifyPackage decides the trust state of a package from the manifest bytes
// exactly as they exist on disk.
//
// What the signature covers: the canonical form of the manifest with the
// signature field removed. Because a verified manifest is required to pin the
// SHA-256 of the executable, the signature transitively covers the executable
// bytes. It does NOT cover any other file in the package directory.
func (store *TrustStore) VerifyPackage(manifestJSON []byte, manifest Manifest) (TrustDecision, error) {
	if manifest.Signature == nil || strings.TrimSpace(manifest.Signature.Value) == "" {
		return TrustDecision{Status: TrustUnsigned, Reason: "package carries no signature"}, nil
	}
	signature := *manifest.Signature
	decision := TrustDecision{Status: TrustUnverified, KeyID: strings.TrimSpace(signature.KeyID), Publisher: manifest.Publisher}
	if strings.ToLower(strings.TrimSpace(signature.Algorithm)) != "ed25519" {
		decision.Reason = "unsupported signature algorithm"
		return decision, nil
	}
	// A signature that does not commit to the payload digest would only
	// authenticate metadata, so it is never accepted as verified.
	if manifest.Checksum == nil || strings.TrimSpace(manifest.Checksum.Value) == "" {
		decision.Reason = "signed manifest does not pin the executable checksum"
		return decision, nil
	}
	if decision.KeyID == "" {
		decision.Reason = "signature does not name a key id"
		return decision, nil
	}
	keys, err := store.Keys()
	if err != nil {
		return TrustDecision{}, err
	}
	var trusted *PublisherKey
	for index := range keys {
		if keys[index].KeyID == decision.KeyID {
			trusted = &keys[index]
			break
		}
	}
	if trusted == nil {
		decision.Reason = "publisher key is not in the local trust store"
		return decision, nil
	}
	if trusted.Publisher != "" && !strings.EqualFold(strings.TrimSpace(trusted.Publisher), strings.TrimSpace(manifest.Publisher)) {
		decision.Reason = "trusted key is registered for a different publisher"
		return decision, nil
	}
	publicKey, err := decodeKeyMaterial(trusted.PublicKey)
	if err != nil {
		decision.Reason = "trusted key material is unusable"
		return decision, nil
	}
	raw, err := decodeSignatureValue(signature.Value)
	if err != nil {
		decision.Reason = "signature value is not decodable"
		return decision, nil
	}
	if len(raw) != ed25519.SignatureSize {
		decision.Reason = "signature has an unexpected length"
		return decision, nil
	}
	payload, err := SigningPayload(manifestJSON)
	if err != nil {
		decision.Reason = "manifest cannot be canonicalized for verification"
		return decision, nil
	}
	if !ed25519.Verify(ed25519.PublicKey(publicKey), payload, raw) {
		decision.Reason = "signature does not match the manifest"
		return decision, nil
	}
	decision.Status = TrustVerified
	decision.Publisher = manifest.Publisher
	decision.Reason = "verified against trusted publisher key " + trusted.KeyID
	return decision, nil
}

func decodeSignatureValue(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("empty signature")
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return decoded, nil
	}
	if decoded, err := hex.DecodeString(value); err == nil {
		return decoded, nil
	}
	return nil, errors.New("signature is neither base64 nor hex")
}

// SigningPayload returns the exact bytes an ed25519 publisher key signs.
//
// The manifest is re-encoded canonically (object keys sorted, no insignificant
// whitespace, numeric literals preserved) with the "signature" member removed,
// then prefixed with a domain separator. Canonicalization means a signature
// survives reformatting of plugin.json but breaks on any semantic change,
// including a changed checksum, capability declaration, or executable path.
func SigningPayload(manifestJSON []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(manifestJSON))
	decoder.UseNumber()
	var document map[string]any
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: canonicalize manifest: %v", ErrInvalidManifest, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: manifest contains trailing JSON", ErrInvalidManifest)
	}
	if document == nil {
		return nil, fmt.Errorf("%w: manifest must be a JSON object", ErrInvalidManifest)
	}
	delete(document, "signature")
	// encoding/json sorts map keys, so this encoding is canonical for any
	// permutation of the source document.
	canonical, err := json.Marshal(document)
	if err != nil {
		return nil, fmt.Errorf("%w: canonicalize manifest: %v", ErrInvalidManifest, err)
	}
	payload := make([]byte, 0, len(signingDomain)+len(canonical))
	payload = append(payload, signingDomain...)
	payload = append(payload, canonical...)
	return payload, nil
}

// SignManifest produces the base64 signature value for a manifest. It exists
// so packaging tooling and tests use exactly the same canonicalization as
// verification does.
func SignManifest(privateKey ed25519.PrivateKey, manifestJSON []byte) (string, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: ed25519 private key must be %d bytes", ErrPublisherKeyInvalid, ed25519.PrivateKeySize)
	}
	payload, err := SigningPayload(manifestJSON)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)), nil
}
