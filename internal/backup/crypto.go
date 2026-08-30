package backup

import (
	"bufio"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"golang.org/x/crypto/scrypt"
)

const (
	// Format identifies the current on-disk envelope and manifest format.
	Format = "yuri-encrypted-backup"
	// Version is the manifest version recorded inside the payload. It is
	// distinct from the envelope version (see envelopeVersionSealed and
	// envelopeVersionChunked): the envelope framing changed for H-15 while the
	// manifest schema did not, so bumping this would have invalidated the
	// manifests of existing archives for no reason.
	Version = 1

	// The defaults are deliberately finite. Callers can lower them for a
	// narrower import/export boundary, but cannot raise them above the hard
	// implementation bounds.
	DefaultMaxArchiveBytes   int64 = 256 << 20
	DefaultMaxPlaintextBytes int64 = 256 << 20
	DefaultMaxDatabaseBytes  int64 = 128 << 20
	DefaultMaxConfigBytes    int64 = 1 << 20
	DefaultMaxBlobBytes      int64 = 16 << 20
	DefaultMaxBlobTotalBytes int64 = 128 << 20
	DefaultMaxBlobs                = 4096
	DefaultMaxPathBytes            = 4096
	DefaultMaxManifestBytes  int64 = 1 << 20

	hardMaxArchiveBytes   int64 = 1 << 30
	hardMaxPlaintextBytes int64 = 1 << 30
	hardMaxDatabaseBytes  int64 = 512 << 20
	hardMaxConfigBytes    int64 = 16 << 20
	hardMaxBlobBytes      int64 = 128 << 20
	hardMaxBlobTotalBytes int64 = 512 << 20
	hardMaxBlobs                = 16384
	hardMaxPathBytes            = 16 << 10

	maxEnvelopeHeaderBytes = 64 << 10
	maxPassphraseBytes     = 4096
	maxEntryNameBytes      = 1024
	copyBufferBytes        = 32 << 10

	// envelopeVersionSealed is the original envelope: the whole zip payload is
	// a single AES-256-GCM ciphertext. It is still read, but no longer written,
	// because sealing and opening it require the entire payload in memory.
	envelopeVersionSealed = 1
	// envelopeVersionChunked is the streaming envelope written since H-15. The
	// payload is a sequence of independently sealed frames, so neither export
	// nor restore ever holds more than one frame of plaintext.
	envelopeVersionChunked = 2

	// framingSTREAM names the chunk construction in the envelope header. It is
	// the discriminator restore dispatches on, alongside the version.
	framingSTREAM = "stream-aes-256-gcm"

	// defaultChunkPlaintextBytes is the frame size used by export. Frames are
	// the unit of buffered plaintext on both sides, so this is the dominant
	// term in the streaming memory profile.
	defaultChunkPlaintextBytes = 1 << 20
	// minChunkPlaintextBytes and maxChunkPlaintextBytes bound the frame size an
	// archive header may declare. The upper bound matters: chunk_size is
	// attacker-controlled input that sizes a restore-side buffer.
	minChunkPlaintextBytes = 4 << 10
	maxChunkPlaintextBytes = 8 << 20

	gcmTagBytes   = 16
	gcmNonceBytes = 12
	// chunkNoncePrefixBytes is how much of the header nonce is reused verbatim
	// in every frame nonce. The remaining bytes carry the frame counter and the
	// final-frame marker.
	chunkNoncePrefixBytes = 3
)

var envelopeMagic = [8]byte{'Y', 'U', 'R', 'I', 'B', 'K', 'P', '1'}

// chunkNonce derives the nonce for one frame.
//
// Layout (12 bytes): prefix[0:3] || uint64be(index) || finalMarker.
//
// The prefix comes from the per-archive random header nonce; the counter binds
// a frame to its position; the trailing marker byte is 1 only on the final
// frame. Together with the header being in every frame's associated data this
// is the STREAM construction: a frame cannot be moved to another index
// (counter), cannot be dropped from the end (marker), and cannot be spliced in
// from a different archive (associated data covers the unique salt and nonce).
func chunkNonce(prefix []byte, index uint64, final bool) [gcmNonceBytes]byte {
	var nonce [gcmNonceBytes]byte
	copy(nonce[:chunkNoncePrefixBytes], prefix)
	binary.BigEndian.PutUint64(nonce[chunkNoncePrefixBytes:chunkNoncePrefixBytes+8], index)
	if final {
		nonce[gcmNonceBytes-1] = 1
	}
	return nonce
}

// chunkSealer frames a plaintext stream into sealed frames on the way out. It
// buffers at most one frame; Close seals whatever remains, always emitting a
// frame marked final (an empty one when the plaintext ended on a boundary).
type chunkSealer struct {
	ctx        context.Context
	aead       cipher.AEAD
	out        io.Writer
	prefix     [chunkNoncePrefixBytes]byte
	associated []byte
	plain      []byte
	filled     int
	sealed     []byte
	index      uint64
	remaining  int64
	closed     bool
}

func newChunkSealer(ctx context.Context, out io.Writer, aead cipher.AEAD, nonce, associated []byte, chunkSize int, budget int64) *chunkSealer {
	sealer := &chunkSealer{
		ctx: ctx, aead: aead, out: out,
		associated: append([]byte(nil), associated...),
		plain:      make([]byte, chunkSize),
		sealed:     make([]byte, 0, chunkSize+aead.Overhead()),
		remaining:  budget,
	}
	copy(sealer.prefix[:], nonce)
	return sealer
}

func (s *chunkSealer) Write(content []byte) (int, error) {
	if s.closed {
		return 0, fmt.Errorf("%w: write after final frame", ErrInvalidArchive)
	}
	var consumed int
	for len(content) > 0 {
		if s.filled == len(s.plain) {
			if err := s.flush(false); err != nil {
				return consumed, err
			}
		}
		n := copy(s.plain[s.filled:], content)
		s.filled += n
		consumed += n
		content = content[n:]
	}
	return consumed, nil
}

// Close seals the trailing frame. It is safe to call more than once.
func (s *chunkSealer) Close() error {
	if s.closed {
		return nil
	}
	if err := s.flush(true); err != nil {
		return err
	}
	s.closed = true
	return nil
}

func (s *chunkSealer) flush(final bool) error {
	if err := checkContext(s.ctx); err != nil {
		return err
	}
	nonce := chunkNonce(s.prefix[:], s.index, final)
	s.sealed = s.aead.Seal(s.sealed[:0], nonce[:], s.plain[:s.filled], s.associated)
	if int64(len(s.sealed)) > s.remaining {
		return fmt.Errorf("%w: encrypted archive", ErrSizeLimit)
	}
	if _, err := s.out.Write(s.sealed); err != nil {
		return err
	}
	s.remaining -= int64(len(s.sealed))
	s.filled = 0
	s.index++
	return nil
}

// openChunkStream decrypts a framed body into destination.
//
// A frame is final when the source has nothing after it; the final-frame marker
// is part of that frame's nonce, so a stream truncated at a frame boundary
// fails authentication rather than silently decoding as a shorter archive.
func openChunkStream(ctx context.Context, source io.Reader, destination io.Writer, aead cipher.AEAD, nonce, associated []byte, chunkSize int, max int64) (int64, error) {
	if chunkSize < minChunkPlaintextBytes || chunkSize > maxChunkPlaintextBytes {
		return 0, fmt.Errorf("%w: envelope chunk size", ErrInvalidArchive)
	}
	var prefix [chunkNoncePrefixBytes]byte
	copy(prefix[:], nonce)
	reader := bufio.NewReaderSize(source, 64<<10)
	frame := make([]byte, chunkSize+aead.Overhead())
	opened := make([]byte, 0, chunkSize)
	var total int64
	for index := uint64(0); ; index++ {
		if err := checkContext(ctx); err != nil {
			return total, err
		}
		count, readErr := io.ReadFull(reader, frame)
		final := false
		switch {
		case readErr == nil:
			_, peekErr := reader.Peek(1)
			switch {
			case errors.Is(peekErr, io.EOF):
				final = true
			case peekErr != nil:
				return total, fmt.Errorf("%w: read encrypted frame: %v", ErrInvalidArchive, peekErr)
			}
		case errors.Is(readErr, io.ErrUnexpectedEOF):
			final = true
		case errors.Is(readErr, io.EOF):
			// Every well-formed stream ends on a frame marked final, and the
			// loop returns there. Reaching EOF instead means the marked frame
			// was removed.
			return total, fmt.Errorf("%w: encrypted stream ends without a final frame", ErrInvalidArchive)
		default:
			return total, fmt.Errorf("%w: read encrypted frame: %v", ErrInvalidArchive, readErr)
		}
		if count < aead.Overhead() {
			return total, fmt.Errorf("%w: truncated encrypted frame", ErrInvalidArchive)
		}
		frameNonce := chunkNonce(prefix[:], index, final)
		plain, openErr := aead.Open(opened[:0], frameNonce[:], frame[:count], associated)
		if openErr != nil {
			return total, fmt.Errorf("%w: %v", ErrWrongPassphrase, openErr)
		}
		if total > max-int64(len(plain)) {
			return total, fmt.Errorf("%w: decrypted payload", ErrSizeLimit)
		}
		if len(plain) > 0 {
			if _, err := destination.Write(plain); err != nil {
				return total, err
			}
			total += int64(len(plain))
		}
		if final {
			return total, nil
		}
	}
}

// encryptPayloadStream writes a complete chunked envelope to out: magic, header
// length, header, then the framed body produced by writePlaintext.
//
// Nothing here materializes the payload. writePlaintext receives a writer that
// seals and forwards one frame at a time, so peak memory is one frame plus the
// scrypt working set, independent of archive size.
func encryptPayloadStream(ctx context.Context, out io.Writer, passphrase string, params KDFParams, limits Limits, writePlaintext func(io.Writer) error) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	params = params.withDefaults()
	if err := params.Validate(); err != nil {
		return err
	}
	if len(params.Salt) == 0 {
		params.Salt = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, params.Salt); err != nil {
			return fmt.Errorf("generate backup salt: %w", err)
		}
	}
	nonce := make([]byte, gcmNonceBytes)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return fmt.Errorf("generate backup nonce: %w", err)
	}
	key, err := deriveKey(ctx, passphrase, params)
	if err != nil {
		return err
	}
	defer zeroBytes(key)
	gcm, err := newGCM(key)
	if err != nil {
		return err
	}
	header := envelopeHeader{
		Format: Format, Version: envelopeVersionChunked, Cipher: "AES-256-GCM", KDF: "scrypt",
		N: params.N, R: params.R, P: params.P,
		Salt:      base64.RawStdEncoding.EncodeToString(params.Salt),
		Nonce:     base64.RawStdEncoding.EncodeToString(nonce),
		Framing:   framingSTREAM,
		ChunkSize: defaultChunkPlaintextBytes,
	}
	headerBytes, err := json.Marshal(header)
	if err != nil {
		return fmt.Errorf("encode backup header: %w", err)
	}
	if len(headerBytes) > maxEnvelopeHeaderBytes {
		return fmt.Errorf("%w: envelope header", ErrSizeLimit)
	}
	prefix := int64(len(envelopeMagic)) + 4 + int64(len(headerBytes))
	if prefix >= limits.MaxArchiveBytes {
		return fmt.Errorf("%w: encrypted archive", ErrSizeLimit)
	}
	if err := checkContext(ctx); err != nil {
		return err
	}
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(headerBytes)))
	for _, part := range [][]byte{envelopeMagic[:], size[:], headerBytes} {
		if _, err := out.Write(part); err != nil {
			return fmt.Errorf("write backup envelope: %w", err)
		}
	}
	associated := make([]byte, 0, len(envelopeMagic)+len(headerBytes))
	associated = append(associated, envelopeMagic[:]...)
	associated = append(associated, headerBytes...)

	sealer := newChunkSealer(ctx, out, gcm, nonce, associated, defaultChunkPlaintextBytes, limits.MaxArchiveBytes-prefix)
	if err := writePlaintext(sealer); err != nil {
		return err
	}
	if err := sealer.Close(); err != nil {
		return err
	}
	return checkContext(ctx)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES-256 cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	return gcm, nil
}

// envelopeHeader is the cleartext prologue of an archive. It is covered by the
// AEAD associated data of every frame, so none of it can be edited in place.
//
// Version 1 archives (single seal) carry plaintext_size/ciphertext_size and no
// framing/chunk_size. Version 2 archives (chunked) carry framing/chunk_size and
// leave the sizes at zero, because the payload size is not known when the
// header has to be written and authenticated.
type envelopeHeader struct {
	Format         string `json:"format"`
	Version        int    `json:"version"`
	Cipher         string `json:"cipher"`
	KDF            string `json:"kdf"`
	N              int    `json:"n"`
	R              int    `json:"r"`
	P              int    `json:"p"`
	Salt           string `json:"salt"`
	Nonce          string `json:"nonce"`
	PlaintextSize  int64  `json:"plaintext_size"`
	CiphertextSize int64  `json:"ciphertext_size"`
	Framing        string `json:"framing,omitempty"`
	ChunkSize      int    `json:"chunk_size,omitempty"`
}

func deriveKey(ctx context.Context, passphrase string, params KDFParams) ([]byte, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	password := []byte(passphrase)
	defer zeroBytes(password)
	salt := append([]byte(nil), params.Salt...)
	defer zeroBytes(salt)
	// scrypt.Key has no cancellation hook. Keep it synchronous so a canceled
	// call cannot leave an expensive worker running after the API returns.
	key, err := scrypt.Key(password, salt, params.N, params.R, params.P, 32)
	if err != nil {
		return nil, fmt.Errorf("%w: derive key: %v", ErrInvalidArchive, err)
	}
	if err := checkContext(ctx); err != nil {
		zeroBytes(key)
		return nil, err
	}
	return key, nil
}

func digestBytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
