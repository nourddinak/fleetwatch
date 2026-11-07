package email

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Raw messages and uploads are files under the mail directory, sealed with the
// dashboard's secret key (secret.Box, AES-256-GCM): a copied disk or backup is unreadable
// without OPS_DATA_DIR/secret.key (or OPS_SECRET_KEY).

const rawAD = "email.raw"

// writeFile seals data into rel (relative to the mail dir), atomically.
func (s *Service) writeFile(rel string, data []byte) error {
	path, err := s.path(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(s.box.Seal(data, rawAD)); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()
	return os.Rename(tmp, path)
}

func (s *Service) readFile(rel string) ([]byte, error) {
	path, err := s.path(rel)
	if err != nil {
		return nil, err
	}
	sealed, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data, err := s.box.Open(sealed, rawAD)
	if err != nil {
		return nil, fmt.Errorf("%s cannot be decrypted (was the secret key changed?)", rel)
	}
	return data, nil
}

func (s *Service) removeFile(rel string) {
	if path, err := s.path(rel); err == nil {
		os.Remove(path)
	}
}

// path keeps every file inside the mail directory: names come from this package, but
// a row edited by hand must still not reach elsewhere.
func (s *Service) path(rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if rel == "" || filepath.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "..") {
		return "", errors.New("bad mail file path")
	}
	return filepath.Join(s.dir, clean), nil
}

func jsonString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func jsonInto(s string, v any) { json.Unmarshal([]byte(s), v) }
