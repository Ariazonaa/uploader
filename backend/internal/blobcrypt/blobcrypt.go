// Package blobcrypt implements chunked authenticated encryption for uploaded
// blobs at rest. The on-disk format is self-describing: given only the file and
// the master key, the plaintext can be recovered and its integrity verified.
//
// Layout:
//
//	Header (28 bytes, cleartext):
//	  [0:4]   magic       = "UEB1"
//	  [4]     version     = 0x01
//	  [5]     algo        = 0x01   (AES-256-GCM)
//	  [6:8]   reserved    = 0x0000
//	  [8:12]  chunkSize   = uint32 big-endian (65536)
//	  [12:28] salt        = 16 random bytes (per-file)
//	Then N sealed chunks. Plaintext chunk size = chunkSize except the final
//	chunk, which is in [0, chunkSize]. Each ciphertext chunk = GCM_Seal(chunk)
//	and is len+16 bytes (the trailing GCM tag).
//
// Every file derives its OWN AES key from the master key and the random 16-byte
// salt (fileKey = HMAC-SHA256(masterKey, salt || label)). Because the key is
// unique per file, the per-chunk nonce only has to be unique WITHIN a file, so
// a simple chunk counter suffices and AES-GCM nonce reuse across files is
// impossible regardless of how many files are stored. (A shared key with a
// small random nonce prefix would risk a birthday collision after enough
// files, which is catastrophic for GCM — hence the per-file subkey.)
//
// Per-chunk nonce (12 bytes) = counterBE(8) || 0x0000 || finalFlag(1). The
// final flag is 0x01 on the last chunk (else 0x00), binding each chunk to its
// position as the terminator so dropping the last chunk (truncation) fails
// authentication.
//
// The empty plaintext encodes as exactly one final chunk of zero bytes, whose
// ciphertext is just the 16-byte tag.
package blobcrypt

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"
)

const (
	headerSize = 28
	saltSize   = 16
	chunkSize  = 65536 // plaintext bytes per chunk
	tagSize    = 16    // AES-GCM authentication tag
	version    = 0x01
	algoAESGCM = 0x01

	// keyDerivationInfo domain-separates the blob key from the raw master key.
	keyDerivationInfo = "uploader-blob-encryption-v1"
	// fileKeyInfo domain-separates the per-file subkey derivation.
	fileKeyInfo = "uploader-blob-file-key-v1"
)

// magic identifies the on-disk container format.
var magic = []byte("UEB1")

// Errors returned when decrypting.
var (
	// ErrBadMagic means the header is not a recognizable UEB1 blob.
	ErrBadMagic = errors.New("blobcrypt: bad magic")
	// ErrUnsupportedVersion means the version/algorithm/chunk-size is unknown.
	ErrUnsupportedVersion = errors.New("blobcrypt: unsupported version")
	// ErrAuth means authentication failed: tampering, truncation or a wrong key.
	ErrAuth = errors.New("blobcrypt: authentication failed")
)

// DeriveKey derives the 32-byte blob master key from an application secret via
// HMAC-SHA256(secret, keyDerivationInfo). This domain-separates the blob key
// from other uses of the same secret (e.g. the session block key).
func DeriveKey(secret []byte) []byte {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(keyDerivationInfo))
	return mac.Sum(nil)
}

// deriveFileKey derives a per-file 32-byte AES key from the master key and the
// file's random salt, so every file encrypts under a distinct key.
func deriveFileKey(master, salt []byte) []byte {
	mac := hmac.New(sha256.New, master)
	mac.Write(salt)
	mac.Write([]byte(fileKeyInfo))
	return mac.Sum(nil)
}

// newGCM builds an AES-256-GCM AEAD from a 32-byte key.
func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, errors.New("blobcrypt: key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// chunkNonce builds the 12-byte per-chunk nonce: counter(8, big-endian) then two
// zero bytes then finalFlag(1). Unique per chunk within a file; combined with the
// per-file key this rules out any GCM nonce reuse.
func chunkNonce(counter uint64, final bool) []byte {
	var n [12]byte
	binary.BigEndian.PutUint64(n[0:8], counter)
	if final {
		n[11] = 0x01
	}
	return n[:]
}

// buildHeader assembles the 28-byte cleartext header for the given salt.
func buildHeader(salt []byte) []byte {
	h := make([]byte, headerSize)
	copy(h[0:4], magic)
	h[4] = version
	h[5] = algoAESGCM
	// h[6:8] reserved stays zero.
	binary.BigEndian.PutUint32(h[8:12], chunkSize)
	copy(h[12:28], salt)
	return h
}

// --- encryption ---

// encryptingReader emits the header followed by sealed chunks, reading the
// source lazily. It buffers at most one chunk of output plus a single carry
// byte of read-ahead (used to decide whether a full chunk is the last one).
type encryptingReader struct {
	src        io.Reader
	gcm        cipher.AEAD
	counter    uint64
	out        bytes.Buffer // pending output (header, then one sealed chunk)
	carry      byte         // first byte of the next chunk (read-ahead)
	carryValid bool
	finished   bool  // final chunk has been sealed
	err        error // sticky read error from the source
}

// NewEncryptingReader returns a reader that yields the encrypted container for
// src on demand: the header first, then AES-256-GCM sealed chunks. It composes
// with a streaming writer (e.g. storage.Put) without a second temp file. key is
// the blob master key; a random per-file subkey is derived internally.
func NewEncryptingReader(src io.Reader, key []byte) (io.Reader, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, err
	}
	gcm, err := newGCM(deriveFileKey(key, salt))
	if err != nil {
		return nil, err
	}
	e := &encryptingReader{src: src, gcm: gcm}
	e.out.Write(buildHeader(salt))
	return e, nil
}

func (e *encryptingReader) Read(p []byte) (int, error) {
	for e.out.Len() == 0 && !e.finished && e.err == nil {
		e.err = e.fill()
	}
	if e.out.Len() > 0 {
		return e.out.Read(p)
	}
	if e.err != nil {
		return 0, e.err
	}
	return 0, io.EOF
}

// fill seals and buffers the next chunk. It reads one plaintext chunk, then
// peeks a single byte to learn whether that chunk is the last one so it can set
// the final flag (and avoid emitting a spurious trailing empty chunk when the
// plaintext length is an exact multiple of chunkSize).
func (e *encryptingReader) fill() error {
	buf := make([]byte, chunkSize)
	start := 0
	if e.carryValid {
		buf[0] = e.carry
		start = 1
		e.carryValid = false
	}
	n, err := io.ReadFull(e.src, buf[start:])
	total := start + n

	if err == io.EOF || err == io.ErrUnexpectedEOF {
		// Source exhausted while filling this chunk → it is the final chunk.
		e.seal(buf[:total], true)
		return nil
	}
	if err != nil {
		return err
	}

	// A full chunk was read. Peek one byte to decide whether more data follows.
	var one [1]byte
	if _, perr := io.ReadFull(e.src, one[:]); perr == io.EOF {
		e.seal(buf[:total], true) // nothing more → this chunk is final
		return nil
	} else if perr != nil {
		return perr
	}
	// More data exists; stash the peeked byte and emit a non-final chunk.
	e.carry = one[0]
	e.carryValid = true
	e.seal(buf[:total], false)
	return nil
}

// seal writes one GCM-sealed chunk to the output buffer.
func (e *encryptingReader) seal(plain []byte, final bool) {
	sealed := e.gcm.Seal(nil, chunkNonce(e.counter, final), plain, nil)
	e.counter++
	e.out.Write(sealed)
	if final {
		e.finished = true
	}
}

// --- decryption ---

// decryptingReader exposes the plaintext of an encrypted blob as a seekable,
// closeable stream. It validates and decrypts one chunk at a time, caching the
// most recently decrypted chunk so sequential reads don't re-decrypt.
type decryptingReader struct {
	src       io.ReadSeeker
	gcm       cipher.AEAD
	plainSize int64
	pos       int64 // current plaintext offset

	cachedIdx int64 // index of the cached chunk, -1 when none
	cached    []byte
}

// NewReader validates the header of an encrypted blob and returns a seekable
// reader over its plaintext. key is the blob master key (the per-file subkey is
// re-derived from the salt in the header). plainSize is the authoritative
// decrypted length (stored alongside the blob), used to locate the final chunk
// and answer Seek(0, io.SeekEnd).
func NewReader(src io.ReadSeeker, key []byte, plainSize int64) (io.ReadSeekCloser, error) {
	if plainSize < 0 {
		return nil, errors.New("blobcrypt: negative plainSize")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	if _, err := io.ReadFull(src, header); err != nil {
		return nil, ErrBadMagic
	}
	if !bytes.Equal(header[0:4], magic) {
		return nil, ErrBadMagic
	}
	if header[4] != version || header[5] != algoAESGCM {
		return nil, ErrUnsupportedVersion
	}
	if binary.BigEndian.Uint32(header[8:12]) != chunkSize {
		return nil, ErrUnsupportedVersion
	}
	gcm, err := newGCM(deriveFileKey(key, header[12:28]))
	if err != nil {
		return nil, err
	}
	return &decryptingReader{
		src:       src,
		gcm:       gcm,
		plainSize: plainSize,
		cachedIdx: -1,
	}, nil
}

// lastIndex is the 0-based index of the final chunk.
func (d *decryptingReader) lastIndex() int64 {
	if d.plainSize <= 0 {
		return 0
	}
	return (d.plainSize - 1) / chunkSize
}

// chunkPlainLen is the plaintext length of chunk i.
func (d *decryptingReader) chunkPlainLen(i int64) int {
	if i < d.lastIndex() {
		return chunkSize
	}
	if d.plainSize <= 0 {
		return 0
	}
	return int(d.plainSize - i*chunkSize)
}

// loadChunk reads, authenticates and decrypts chunk i, caching the result.
func (d *decryptingReader) loadChunk(i int64) ([]byte, error) {
	if d.cachedIdx == i && d.cached != nil {
		return d.cached, nil
	}
	cipherLen := d.chunkPlainLen(i) + tagSize
	off := int64(headerSize) + i*(chunkSize+tagSize)
	if _, err := d.src.Seek(off, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, cipherLen)
	if _, err := io.ReadFull(d.src, buf); err != nil {
		// A short read means the blob was truncated (e.g. the last chunk was
		// dropped) — treat it as an authentication failure, not a soft EOF.
		return nil, ErrAuth
	}
	final := i == d.lastIndex()
	// #nosec G115 -- i is a non-negative chunk index bounded by the file size.
	plain, err := d.gcm.Open(nil, chunkNonce(uint64(i), final), buf, nil)
	if err != nil {
		return nil, ErrAuth
	}
	d.cachedIdx = i
	d.cached = plain
	return plain, nil
}

func (d *decryptingReader) Read(p []byte) (int, error) {
	if d.pos >= d.plainSize {
		return 0, io.EOF
	}
	idx := d.pos / chunkSize
	intra := int(d.pos % chunkSize)
	chunk, err := d.loadChunk(idx)
	if err != nil {
		return 0, err
	}
	if intra >= len(chunk) {
		return 0, io.EOF
	}
	n := copy(p, chunk[intra:])
	d.pos += int64(n)
	return n, nil
}

func (d *decryptingReader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = d.pos + offset
	case io.SeekEnd:
		abs = d.plainSize + offset
	default:
		return 0, errors.New("blobcrypt: invalid whence")
	}
	if abs < 0 {
		return 0, errors.New("blobcrypt: negative position")
	}
	d.pos = abs
	return abs, nil
}

// Close closes the underlying source if it is a Closer.
func (d *decryptingReader) Close() error {
	if c, ok := d.src.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
