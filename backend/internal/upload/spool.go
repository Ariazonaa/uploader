package upload

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"

	"uploader/internal/media"
)

// Spool encryption for uploads that will be stored encrypted.
//
// reserveSlug spools every upload to a temp file, and the temp dir is a
// persistent volume: a process killed mid-upload leaves the file behind until
// the janitor's sweep hours later, and its blocks are never wiped. For an
// upload the user asked to have encrypted at rest that file held the whole
// plaintext, which is exactly what the encryption is there to keep off the
// disk (tus already encrypts its scratch for the same reason, and then
// decrypted it into this spool).
//
// Such a spool is AES-CTR encrypted under a random key that exists only in
// this process's memory. Nothing wraps or stores the key: the spool does not
// outlive the request, so after a crash it is unreadable by design. CTR does
// not authenticate; the spool never leaves the server and the stored blob is
// authenticated (blobcrypt).

// ctrFile reads and writes a file through an AES-CTR keystream positioned by
// byte offset, so it can be read back from the start and rewound.
type ctrFile struct {
	f      *os.File
	block  cipher.Block
	stream cipher.Stream // positioned at the file offset
}

// newSpoolBlock returns a cipher under a fresh random key.
func newSpoolBlock() (cipher.Block, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	return aes.NewCipher(key)
}

// newCTRFile wraps f, which must be positioned at offset 0.
func newCTRFile(f *os.File, block cipher.Block) *ctrFile {
	return &ctrFile{f: f, block: block, stream: ctrStreamAt(block, 0)}
}

// ctrStreamAt returns block's keystream positioned at byte offset off. The
// counter block is the block index; the key is used for this one file only.
func ctrStreamAt(block cipher.Block, off int64) cipher.Stream {
	var iv [aes.BlockSize]byte
	// #nosec G115 -- off is a file offset and never negative.
	binary.BigEndian.PutUint64(iv[8:], uint64(off/aes.BlockSize))
	stream := cipher.NewCTR(block, iv[:])
	if skip := off % aes.BlockSize; skip > 0 {
		var discard [aes.BlockSize]byte
		stream.XORKeyStream(discard[:skip], discard[:skip])
	}
	return stream
}

func (c *ctrFile) Read(p []byte) (int, error) {
	n, err := c.f.Read(p)
	c.stream.XORKeyStream(p[:n], p[:n])
	return n, err
}

func (c *ctrFile) Write(p []byte) (int, error) {
	buf := make([]byte, len(p)) // Write must not modify p
	c.stream.XORKeyStream(buf, p)
	n, err := c.f.Write(buf)
	if n < len(p) {
		// The keystream is past what reached the file; put it back in step.
		if pos, serr := c.f.Seek(0, io.SeekCurrent); serr == nil {
			c.stream = ctrStreamAt(c.block, pos)
		} else if err == nil {
			err = serr
		}
	}
	return n, err
}

func (c *ctrFile) Seek(offset int64, whence int) (int64, error) {
	pos, err := c.f.Seek(offset, whence)
	if err != nil {
		return pos, err
	}
	c.stream = ctrStreamAt(c.block, pos)
	return pos, nil
}

// Truncate empties the file for a rewrite from the start. The rewrite gets a
// new key: the same keystream over two different contents would let whoever
// recovers both versions of a block from the disk cancel the key out.
func (c *ctrFile) Truncate(size int64) error {
	if size != 0 {
		return errors.New("spool: only truncation to zero is supported")
	}
	block, err := newSpoolBlock()
	if err != nil {
		return err
	}
	if err := c.f.Truncate(0); err != nil {
		return err
	}
	pos, err := c.f.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	c.block, c.stream = block, ctrStreamAt(block, pos)
	return nil
}

// sanitizeEncryptedSpool is media.SanitizeJPEG for a spool encrypted under
// srcBlock: it writes the metadata-free JPEG to a new temp file, encrypted too,
// and returns that file's path and cipher. The caller owns (and removes) it.
func sanitizeEncryptedSpool(ctx context.Context, srcPath string, srcBlock cipher.Block) (string, cipher.Block, error) {
	block, err := newSpoolBlock()
	if err != nil {
		return "", nil, err
	}
	src, err := os.Open(srcPath) // #nosec G304 -- our own temp file
	if err != nil {
		return "", nil, err
	}
	defer src.Close()
	out, err := os.CreateTemp("", "uploader-*")
	if err != nil {
		return "", nil, err
	}
	outPath := out.Name()
	enc := newCTRFile(out, block)
	err = media.SanitizeJPEGStream(ctx, enc, newCTRFile(src, srcBlock))
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(outPath)
		return "", nil, err
	}
	return outPath, enc.block, nil
}
