package media

import (
	"io"
	"os"
)

// writeTemp copies r into a temporary file and returns its path.
func writeTemp(r io.Reader) (string, error) {
	tmp, err := os.CreateTemp("", "uploader-media-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

func cleanupTemp(path string) {
	if path != "" {
		_ = os.Remove(path)
	}
}
