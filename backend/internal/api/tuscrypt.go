package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"

	"uploader/internal/upload"
)

// Scratch encryption for tus uploads that will be stored encrypted. Their bytes
// would otherwise sit in plaintext under .tus/ in the storage root for the
// whole transfer, and up to 24h after an abandoned one, and so end up in every
// backup of that root.
//
// Each such upload gets a random AES-256 data key. The data file is AES-CTR
// encrypted with it, the keystream positioned by byte offset, so chunks append
// (and resume) at any offset and the file's size stays the upload offset. The
// data key sits in the upload's .json, wrapped with AES-GCM under a key derived
// from the server's blob-encryption key, so an upload survives a restart while
// the scratch is useless without that key. CTR does not authenticate; the
// scratch never leaves the server, and the stored blob is authenticated
// (blobcrypt).

// tusScratchKeyInfo domain-separates the wrapping key from other uses of the
// blob-encryption key.
const tusScratchKeyInfo = "uploader-tus-scratch-key-v1"

// errTusScratchKey means an upload's data key could not be unwrapped, e.g.
// because the server's encryption key changed since the upload was created.
var errTusScratchKey = errors.New("tus scratch key does not unwrap")

func tusWrapAEAD(master []byte) (cipher.AEAD, error) {
	mac := hmac.New(sha256.New, master)
	mac.Write([]byte(tusScratchKeyInfo))
	block, err := aes.NewCipher(mac.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// newTusScratchKey returns a fresh data key for upload id and its wrapped form
// for the upload's .json. The upload id is bound in as additional data, so a
// wrapped key copied into another upload's record does not unwrap.
func newTusScratchKey(master []byte, id string) ([]byte, string, error) {
	aead, err := tusWrapAEAD(master)
	if err != nil {
		return nil, "", err
	}
	key := make([]byte, 32)
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(key); err != nil {
		return nil, "", err
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	sealed := aead.Seal(nonce, nonce, key, []byte(id))
	return key, base64.StdEncoding.EncodeToString(sealed), nil
}

// unwrapTusScratchKey recovers the data key of upload id.
func unwrapTusScratchKey(master []byte, id, wrapped string) ([]byte, error) {
	aead, err := tusWrapAEAD(master)
	if err != nil {
		return nil, err
	}
	sealed, err := base64.StdEncoding.DecodeString(wrapped)
	if err != nil || len(sealed) < aead.NonceSize() {
		return nil, errTusScratchKey
	}
	key, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], []byte(id))
	if err != nil || len(key) != 32 {
		return nil, errTusScratchKey
	}
	return key, nil
}

// tusStreamAt returns the AES-CTR keystream of a data file positioned at byte
// offset off. The counter block is the block index, which is safe because the
// key belongs to this one upload and the file is append-only: no position is
// ever encrypted twice.
func tusStreamAt(key []byte, off int64) (cipher.Stream, error) {
	if off < 0 {
		return nil, errors.New("negative offset")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	var iv [aes.BlockSize]byte
	// #nosec G115 -- off is non-negative (checked above).
	binary.BigEndian.PutUint64(iv[8:], uint64(off/aes.BlockSize))
	stream := cipher.NewCTR(block, iv[:])
	if skip := off % aes.BlockSize; skip > 0 {
		var discard [aes.BlockSize]byte
		stream.XORKeyStream(discard[:skip], discard[:skip])
	}
	return stream, nil
}

// tusScratchStream returns the keystream of an encrypted upload's data file at
// offset off. Without an encryption key it returns
// upload.ErrEncryptionUnavailable: such an upload can never be stored.
func (s *Server) tusScratchStream(id string, info tusInfo, off int64) (cipher.Stream, error) {
	if len(s.encKey()) == 0 {
		return nil, upload.ErrEncryptionUnavailable
	}
	key, err := unwrapTusScratchKey(s.encKey(), id, info.ScratchKey)
	if err != nil {
		return nil, err
	}
	return tusStreamAt(key, off)
}

// tusScratchWriter wraps the data file for appending at offset off: encrypting
// for an upload with a scratch key, as is otherwise.
func (s *Server) tusScratchWriter(id string, info tusInfo, off int64, f io.Writer) (io.Writer, error) {
	if info.ScratchKey == "" {
		return f, nil
	}
	stream, err := s.tusScratchStream(id, info, off)
	if err != nil {
		return nil, err
	}
	return cipher.StreamWriter{S: stream, W: f}, nil
}

// tusScratchReader wraps the data file for reading it from the start: the
// plaintext for an upload with a scratch key, as is otherwise.
func (s *Server) tusScratchReader(id string, info tusInfo, f io.Reader) (io.Reader, error) {
	if info.ScratchKey == "" {
		return f, nil
	}
	stream, err := s.tusScratchStream(id, info, 0)
	if err != nil {
		return nil, err
	}
	return cipher.StreamReader{S: stream, R: f}, nil
}
