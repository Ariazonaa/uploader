package upload

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// What an encrypted spool holds on disk is not the upload, and reads back as
// it through the same cipher, from the start and after a seek.
func TestCTRFileRoundTrip(t *testing.T) {
	plain := bytes.Repeat([]byte("secret upload contents "), 1000) // not a block multiple
	block, err := newSpoolBlock()
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(t.TempDir(), "spool")
	f, err := os.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	w := newCTRFile(f, block)
	// Uneven writes, so the keystream has to carry over block boundaries.
	for off := 0; off < len(plain); {
		n := min(37, len(plain)-off)
		if _, err := w.Write(plain[off : off+n]); err != nil {
			t.Fatal(err)
		}
		off += n
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	onDisk, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	if len(onDisk) != len(plain) {
		t.Fatalf("spool is %d bytes, want the plaintext length %d", len(onDisk), len(plain))
	}
	if bytes.Contains(onDisk, []byte("secret upload")) {
		t.Fatal("the spool holds plaintext")
	}

	f, err = os.Open(name)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	r := newCTRFile(f, block)
	got, err := io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("read back %d bytes (err %v), want the plaintext", len(got), err)
	}
	const off = 1234
	if _, err := r.Seek(off, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err = io.ReadAll(r)
	if err != nil || !bytes.Equal(got, plain[off:]) {
		t.Fatalf("read after seek: %d bytes (err %v), want the plaintext from %d", len(got), err, off)
	}
}

// A rewrite after Truncate uses a new key, and reads back under it.
func TestCTRFileTruncateRekeys(t *testing.T) {
	block, err := newSpoolBlock()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.CreateTemp(t.TempDir(), "spool")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	c := newCTRFile(f, block)
	if _, err := c.Write([]byte("first version")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	if err := c.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if c.block == block {
		t.Fatal("Truncate kept the key")
	}
	if _, err := c.Write([]byte("second")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(c)
	if err != nil || string(got) != "second" {
		t.Fatalf("read back %q (err %v), want %q", got, err, "second")
	}
}

// Stripping a JPEG from an encrypted spool yields an encrypted spool that
// decrypts to a JPEG; neither file holds the image in the clear.
func TestSanitizeEncryptedSpool(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	t.Setenv("TMP", os.Getenv("TMPDIR"))
	t.Setenv("TEMP", os.Getenv("TMPDIR"))

	var img bytes.Buffer
	if err := jpeg.Encode(&img, image.NewRGBA(image.Rect(0, 0, 32, 32)), nil); err != nil {
		t.Fatal(err)
	}
	block, err := newSpoolBlock()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.CreateTemp("", "uploader-*")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newCTRFile(src, block).Write(img.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := src.Close(); err != nil {
		t.Fatal(err)
	}

	clean, cleanBlock, err := sanitizeEncryptedSpool(context.Background(), src.Name(), block)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(clean)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(onDisk, []byte{0xFF, 0xD8}) {
		t.Fatal("the stripped spool is a plaintext JPEG")
	}
	f, err := os.Open(clean)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := jpeg.Decode(newCTRFile(f, cleanBlock)); err != nil {
		t.Fatalf("the stripped spool does not decrypt to a JPEG: %v", err)
	}
}
