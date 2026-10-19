package blobcrypt

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"testing"
)

// testKey returns a deterministic-length valid 32-byte key.
func testKey(t *testing.T) []byte {
	t.Helper()
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return k
}

// encrypt is a helper: encrypt plain with key, returning the container bytes.
func encrypt(t *testing.T, plain, key []byte) []byte {
	t.Helper()
	r, err := NewEncryptingReader(bytes.NewReader(plain), key)
	if err != nil {
		t.Fatalf("NewEncryptingReader: %v", err)
	}
	enc, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read encrypted: %v", err)
	}
	return enc
}

// expectedChunks returns the number of sealed chunks for a plaintext length.
func expectedChunks(n int) int {
	if n == 0 {
		return 1
	}
	return (n + chunkSize - 1) / chunkSize
}

func TestRoundTrip(t *testing.T) {
	key := testKey(t)
	sizes := []int{0, 1, 65535, 65536, 65537, 131072, 131079, 5 << 20}
	for _, n := range sizes {
		plain := make([]byte, n)
		if _, err := rand.Read(plain); err != nil {
			t.Fatalf("rand: %v", err)
		}
		enc := encrypt(t, plain, key)

		// Output starts with the magic and has the exact expected length.
		if !bytes.HasPrefix(enc, magic) {
			t.Fatalf("n=%d: output does not start with magic", n)
		}
		wantLen := headerSize + expectedChunks(n)*tagSize + n
		if len(enc) != wantLen {
			t.Fatalf("n=%d: len=%d want=%d", n, len(enc), wantLen)
		}

		dr, err := NewReader(bytes.NewReader(enc), key, int64(n))
		if err != nil {
			t.Fatalf("n=%d: NewReader: %v", n, err)
		}
		got, err := io.ReadAll(dr)
		if err != nil {
			t.Fatalf("n=%d: read decrypted: %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("n=%d: round-trip mismatch (got %d bytes)", n, len(got))
		}
		_ = dr.Close()
	}
}

func TestSeek(t *testing.T) {
	key := testKey(t)
	const n = 131079
	plain := make([]byte, n)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand: %v", err)
	}
	enc := encrypt(t, plain, key)

	dr, err := NewReader(bytes.NewReader(enc), key, int64(n))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	defer dr.Close()

	// Seek(0, SeekEnd) must report the plaintext size.
	end, err := dr.Seek(0, io.SeekEnd)
	if err != nil {
		t.Fatalf("seek end: %v", err)
	}
	if end != int64(n) {
		t.Fatalf("SeekEnd = %d, want %d", end, n)
	}

	const m = 4096
	offsets := []int64{0, 100, 65536, 65536 + 10, int64(n) - 1}
	for _, off := range offsets {
		pos, err := dr.Seek(off, io.SeekStart)
		if err != nil {
			t.Fatalf("seek %d: %v", off, err)
		}
		if pos != off {
			t.Fatalf("seek returned %d, want %d", pos, off)
		}
		want := plain[off:min64(off+m, int64(n))]
		got := make([]byte, len(want))
		if _, err := io.ReadFull(dr, got); err != nil {
			t.Fatalf("read at %d: %v", off, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("data at offset %d mismatch", off)
		}
	}

	// Tail read via Seek(-M, SeekEnd).
	const tail = 12345
	if _, err := dr.Seek(-tail, io.SeekEnd); err != nil {
		t.Fatalf("seek tail: %v", err)
	}
	got, err := io.ReadAll(dr)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if !bytes.Equal(got, plain[n-tail:]) {
		t.Fatalf("tail read mismatch")
	}
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func TestTamperFailsAuth(t *testing.T) {
	key := testKey(t)
	plain := make([]byte, 200000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand: %v", err)
	}
	enc := encrypt(t, plain, key)

	// Flip one byte inside the first ciphertext chunk (just past the header).
	enc[headerSize+5] ^= 0x01

	dr, err := NewReader(bytes.NewReader(enc), key, int64(len(plain)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	_, err = io.ReadAll(dr)
	if err != ErrAuth {
		t.Fatalf("tamper: got err=%v, want ErrAuth", err)
	}
}

func TestTruncationFailsAuth(t *testing.T) {
	key := testKey(t)
	// Three chunks: 65536 + 65536 + 7.
	const n = 131079
	plain := make([]byte, n)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand: %v", err)
	}
	enc := encrypt(t, plain, key)

	// Drop the last ciphertext chunk entirely (its plaintext was 7 bytes → 23
	// ciphertext bytes). plainSize is left unchanged, as a real reader would
	// derive it from the DB.
	lastChunkCipher := (n - 2*chunkSize) + tagSize
	truncated := enc[:len(enc)-lastChunkCipher]

	dr, err := NewReader(bytes.NewReader(truncated), key, int64(n))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	_, err = io.ReadAll(dr)
	if err != ErrAuth {
		t.Fatalf("truncation: got err=%v, want ErrAuth", err)
	}
}

func TestWrongKeyFailsAuth(t *testing.T) {
	key := testKey(t)
	plain := make([]byte, 70000)
	if _, err := rand.Read(plain); err != nil {
		t.Fatalf("rand: %v", err)
	}
	enc := encrypt(t, plain, key)

	wrong := testKey(t)
	dr, err := NewReader(bytes.NewReader(enc), wrong, int64(len(plain)))
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	_, err = io.ReadAll(dr)
	if err != ErrAuth {
		t.Fatalf("wrong key: got err=%v, want ErrAuth", err)
	}
}

func TestBadMagic(t *testing.T) {
	key := testKey(t)
	buf := make([]byte, 64)
	copy(buf, []byte("NOPE"))
	binary.BigEndian.PutUint32(buf[8:12], chunkSize)
	buf[4] = version
	buf[5] = algoAESGCM
	if _, err := NewReader(bytes.NewReader(buf), key, 0); err != ErrBadMagic {
		t.Fatalf("bad magic: got %v, want ErrBadMagic", err)
	}
}

func TestUnsupportedVersion(t *testing.T) {
	key := testKey(t)
	buf := make([]byte, 64)
	copy(buf, magic)
	buf[4] = 0x02 // unknown version
	buf[5] = algoAESGCM
	binary.BigEndian.PutUint32(buf[8:12], chunkSize)
	if _, err := NewReader(bytes.NewReader(buf), key, 0); err != ErrUnsupportedVersion {
		t.Fatalf("bad version: got %v, want ErrUnsupportedVersion", err)
	}
}

func TestDeriveKey(t *testing.T) {
	master := make([]byte, 32)
	if _, err := rand.Read(master); err != nil {
		t.Fatalf("rand: %v", err)
	}
	k1 := DeriveKey(master)
	k2 := DeriveKey(master)
	if len(k1) != 32 {
		t.Fatalf("derived key length = %d, want 32", len(k1))
	}
	if !bytes.Equal(k1, k2) {
		t.Fatalf("DeriveKey not deterministic")
	}
	if bytes.Equal(k1, master) {
		t.Fatalf("derived key equals master")
	}
	// A different master yields a different key.
	master2 := make([]byte, 32)
	copy(master2, master)
	master2[0] ^= 0xff
	if bytes.Equal(DeriveKey(master2), k1) {
		t.Fatalf("DeriveKey collision on distinct masters")
	}
}
