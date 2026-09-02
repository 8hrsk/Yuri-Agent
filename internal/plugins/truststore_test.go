package plugins

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestSigningPayloadIsCanonicalAndExcludesSignature(t *testing.T) {
	ordered := []byte(`{"id":"a","name":"b","signature":{"algorithm":"ed25519","key_id":"k","value":"v"},"version":"1.0.0"}`)
	shuffled := []byte("{\n  \"version\": \"1.0.0\",\n  \"name\": \"b\",\n  \"id\": \"a\"\n}\n")
	first, err := SigningPayload(ordered)
	if err != nil {
		t.Fatal(err)
	}
	second, err := SigningPayload(shuffled)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("canonical payloads differ:\n%s\n%s", first, second)
	}
	if string(first) != signingDomain+`{"id":"a","name":"b","version":"1.0.0"}` {
		t.Fatalf("unexpected canonical payload %q", first)
	}
	changed, err := SigningPayload([]byte(`{"id":"a","name":"b","version":"1.0.1"}`))
	if err != nil {
		t.Fatal(err)
	}
	if string(changed) == string(first) {
		t.Fatal("a semantic manifest change must change the signed payload")
	}
}

func TestTrustStoreAddListRemove(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust", TrustStoreFileName)
	store := OpenTrustStore(path)
	keys, err := store.Keys()
	if err != nil || len(keys) != 0 {
		t.Fatalf("fresh store = %#v, %v", keys, err)
	}
	public, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encoded := base64.StdEncoding.EncodeToString(public)
	if _, err := store.Add(PublisherKey{KeyID: "k1", PublicKey: encoded, Publisher: "OrdoAI"}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("trust store permissions = %v", info.Mode().Perm())
	}
	// Re-adding the same material is idempotent, changing it is refused.
	if _, err := store.Add(PublisherKey{KeyID: "k1", PublicKey: encoded, Publisher: "OrdoAI"}); err != nil {
		t.Fatalf("re-adding identical key: %v", err)
	}
	other, _, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Add(PublisherKey{KeyID: "k1", PublicKey: base64.StdEncoding.EncodeToString(other)}); err == nil {
		t.Fatal("silent key replacement was accepted")
	}
	removed, err := store.Remove("k1")
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if removed, err := store.Remove("k1"); err != nil || removed {
		t.Fatalf("second remove = %v, %v", removed, err)
	}
}

func TestVerifyPackageTrustStates(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	store := OpenTrustStore(filepath.Join(t.TempDir(), TrustStoreFileName))
	if _, err := store.Add(PublisherKey{KeyID: "trusted", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI"}); err != nil {
		t.Fatal(err)
	}

	base := Manifest{
		SchemaVersion: ManifestSchemaVersion, ID: "dev.yuri.signed", Name: "Signed", Version: "0.1.0",
		Publisher: "OrdoAI", Executable: "run", ProtocolVersion: ProtocolVersion,
		SupportedOS: []string{"darwin"}, SupportedArch: []string{"arm64"},
		Checksum: &ChecksumMetadata{Algorithm: "sha256", Value: "aa" + repeatHex(62)},
	}

	unsignedContent, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.VerifyPackage(unsignedContent, base)
	if err != nil || decision.Status != TrustUnsigned {
		t.Fatalf("unsigned decision = %#v, %v", decision, err)
	}

	signature, err := SignManifest(private, unsignedContent)
	if err != nil {
		t.Fatal(err)
	}
	signed := base
	signed.Signature = &SignatureMetadata{Algorithm: "ed25519", KeyID: "trusted", Value: signature}
	signedContent, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.VerifyPackage(signedContent, signed)
	if err != nil || decision.Status != TrustVerified {
		t.Fatalf("verified decision = %#v, %v", decision, err)
	}

	// Same signature, different key id: the owner never trusted that key.
	untrusted := signed
	untrusted.Signature = &SignatureMetadata{Algorithm: "ed25519", KeyID: "someone-else", Value: signature}
	untrustedContent, err := json.Marshal(untrusted)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.VerifyPackage(untrustedContent, untrusted)
	if err != nil || decision.Status != TrustUnverified {
		t.Fatalf("untrusted publisher decision = %#v, %v", decision, err)
	}

	// A changed checksum is a changed payload commitment: the signature over
	// the old manifest must no longer verify.
	tampered := signed
	tampered.Checksum = &ChecksumMetadata{Algorithm: "sha256", Value: "bb" + repeatHex(62)}
	tamperedContent, err := json.Marshal(tampered)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.VerifyPackage(tamperedContent, tampered)
	if err != nil || decision.Status != TrustUnverified {
		t.Fatalf("tampered checksum decision = %#v, %v", decision, err)
	}

	// A signature that does not commit to a payload digest is metadata only.
	noChecksum := signed
	noChecksum.Checksum = nil
	noChecksumContent, err := json.Marshal(noChecksum)
	if err != nil {
		t.Fatal(err)
	}
	decision, err = store.VerifyPackage(noChecksumContent, noChecksum)
	if err != nil || decision.Status != TrustUnverified {
		t.Fatalf("checksum-less decision = %#v, %v", decision, err)
	}
}

func repeatHex(length int) string {
	value := make([]byte, length)
	for index := range value {
		value[index] = '0'
	}
	return string(value)
}

// H-14 adversarial cases: a signature that is present but unusable must be
// rejected on its own terms. Every case below carries a trusted key id, so a
// verifier that treated "signature present" as "signature good" — or that
// skipped a signature it could not parse — would report TrustVerified.
func TestVerifyPackageRejectsMalformedAndTruncatedSignatures(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	store := OpenTrustStore(filepath.Join(t.TempDir(), TrustStoreFileName))
	if _, err := store.Add(PublisherKey{KeyID: "trusted", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI"}); err != nil {
		t.Fatal(err)
	}
	base := Manifest{
		SchemaVersion: ManifestSchemaVersion, ID: "dev.yuri.signed", Name: "Signed", Version: "0.1.0",
		Publisher: "OrdoAI", Executable: "run", ProtocolVersion: ProtocolVersion,
		SupportedOS: []string{"darwin"}, SupportedArch: []string{"arm64"},
		Checksum: &ChecksumMetadata{Algorithm: "sha256", Value: "aa" + repeatHex(62)},
	}
	unsignedContent, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	good, err := SignManifest(private, unsignedContent)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(good)
	if err != nil {
		t.Fatal(err)
	}

	for _, testCase := range []struct {
		name      string
		algorithm string
		keyID     string
		value     string
	}{
		{name: "absent value", algorithm: "ed25519", keyID: "trusted", value: ""},
		{name: "whitespace value", algorithm: "ed25519", keyID: "trusted", value: "   "},
		{name: "not base64 or hex", algorithm: "ed25519", keyID: "trusted", value: "!!! not a signature !!!"},
		{name: "truncated by one byte", algorithm: "ed25519", keyID: "trusted", value: base64.StdEncoding.EncodeToString(raw[:len(raw)-1])},
		{name: "truncated to half", algorithm: "ed25519", keyID: "trusted", value: base64.StdEncoding.EncodeToString(raw[:len(raw)/2])},
		{name: "empty byte string", algorithm: "ed25519", keyID: "trusted", value: base64.StdEncoding.EncodeToString(nil)},
		{name: "padded past the signature size", algorithm: "ed25519", keyID: "trusted", value: base64.StdEncoding.EncodeToString(append(append([]byte(nil), raw...), 0))},
		{name: "flipped bit", algorithm: "ed25519", keyID: "trusted", value: base64.StdEncoding.EncodeToString(flipFirstBit(raw))},
		{name: "unsupported algorithm", algorithm: "rsa", keyID: "trusted", value: good},
		{name: "missing algorithm", algorithm: "", keyID: "trusted", value: good},
		{name: "missing key id", algorithm: "ed25519", keyID: "", value: good},
		{name: "unknown key id", algorithm: "ed25519", keyID: "not-trusted", value: good},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			candidate := base
			candidate.Signature = &SignatureMetadata{Algorithm: testCase.algorithm, KeyID: testCase.keyID, Value: testCase.value}
			content, marshalErr := json.Marshal(candidate)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			decision, decisionErr := store.VerifyPackage(content, candidate)
			if decisionErr != nil {
				t.Fatalf("verification errored instead of deciding: %v", decisionErr)
			}
			if decision.Verified() {
				t.Fatalf("an unusable signature was accepted: %#v", decision)
			}
			if decision.Reason == "" {
				t.Fatal("a rejection must say why")
			}
		})
	}
}

func flipFirstBit(value []byte) []byte {
	flipped := append([]byte(nil), value...)
	flipped[0] ^= 0x01
	return flipped
}

// H-14: the signature must cover the manifest bytes that were actually loaded.
// A verifier that re-serialized the parsed struct would authenticate whatever
// the host happened to parse rather than what is on disk, so any manifest
// member the struct drops or normalizes would become freely tamperable.
func TestVerifyPackageVerifiesTheLoadedManifestBytesNotAReserialization(t *testing.T) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	store := OpenTrustStore(filepath.Join(t.TempDir(), TrustStoreFileName))
	if _, err := store.Add(PublisherKey{KeyID: "trusted", PublicKey: base64.StdEncoding.EncodeToString(public), Publisher: "OrdoAI"}); err != nil {
		t.Fatal(err)
	}
	base := Manifest{
		SchemaVersion: ManifestSchemaVersion, ID: "dev.yuri.signed", Name: "Signed", Version: "0.1.0",
		Publisher: "OrdoAI", Executable: "run", ProtocolVersion: ProtocolVersion,
		SupportedOS: []string{"darwin"}, SupportedArch: []string{"arm64"},
		Checksum: &ChecksumMetadata{Algorithm: "sha256", Value: "aa" + repeatHex(62)},
	}
	unsignedContent, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := SignManifest(private, unsignedContent)
	if err != nil {
		t.Fatal(err)
	}
	signed := base
	signed.Signature = &SignatureMetadata{Algorithm: "ed25519", KeyID: "trusted", Value: signature}
	signedContent, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	if decision, decisionErr := store.VerifyPackage(signedContent, signed); decisionErr != nil || !decision.Verified() {
		t.Fatalf("baseline signed manifest = %#v, %v", decision, decisionErr)
	}

	// Reformatting must not break the signature: what is signed is the
	// canonical form of the loaded bytes, not their exact spelling.
	var document map[string]any
	if err := json.Unmarshal(signedContent, &document); err != nil {
		t.Fatal(err)
	}
	reformatted, err := json.MarshalIndent(document, "\t", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(reformatted, signedContent) {
		t.Fatal("the reformatted manifest must differ byte for byte to make this assertion meaningful")
	}
	if decision, decisionErr := store.VerifyPackage(reformatted, signed); decisionErr != nil || !decision.Verified() {
		t.Fatalf("reformatted but semantically identical manifest = %#v, %v", decision, decisionErr)
	}

	// The decisive case: the bytes on disk say one thing and the parsed struct
	// says another. Verification must follow the bytes. Passing the pristine
	// struct alongside tampered bytes is exactly what a re-serializing verifier
	// would accept.
	document["name"] = "Totally Different Plugin"
	tampered, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := store.VerifyPackage(tampered, signed)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verified() {
		t.Fatal("tampered manifest bytes verified against a re-serialization of the parsed struct")
	}

	// And the mirror image: pristine bytes with a struct whose fields were
	// swapped out must still verify, because the struct is not the payload.
	lying := signed
	lying.Name = "Totally Different Plugin"
	if decision, decisionErr := store.VerifyPackage(signedContent, lying); decisionErr != nil || !decision.Verified() {
		t.Fatalf("verification followed the struct instead of the loaded bytes: %#v, %v", decision, decisionErr)
	}
}
