package extract

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/openpgp"
	"golang.org/x/crypto/openpgp/armor"
)

// DecryptSpec decrypts one *.edevice file (an armored OpenPGP symmetric
// message with block type "DESCRIPTOR") into GoLisp source.
func DecryptSpec(data, passphrase []byte) ([]byte, error) {
	block, err := armor.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("armor: %w", err)
	}
	tried := false
	prompt := func(keys []openpgp.Key, symmetric bool) ([]byte, error) {
		if tried {
			return nil, errors.New("bad passphrase")
		}
		tried = true
		return passphrase, nil
	}
	md, err := openpgp.ReadMessage(block.Body, nil, prompt, nil)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(md.UnverifiedBody)
}

// DecryptSpecDir decrypts every *.edevice in src into dst/<name>.lsp.
func DecryptSpecDir(src, dst string, passphrase []byte) (int, error) {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return 0, err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".edevice") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(src, e.Name()))
		if err != nil {
			return n, err
		}
		plain, err := DecryptSpec(raw, passphrase)
		if err != nil {
			return n, fmt.Errorf("%s: %w", e.Name(), err)
		}
		out := filepath.Join(dst, strings.TrimSuffix(e.Name(), ".edevice")+".lsp")
		if err := os.WriteFile(out, plain, 0o644); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}
