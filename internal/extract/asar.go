package extract

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// asarEntry is a node in an Electron asar header.
type asarEntry struct {
	Files    map[string]*asarEntry `json:"files"`
	Size     int64                 `json:"size"`
	Offset   string                `json:"offset"`
	Unpacked bool                  `json:"unpacked"`
	Link     string                `json:"link"`
}

// UnpackAsar extracts an asar archive into dir. Entries marked "unpacked"
// live next to the archive in <archive>.unpacked and are copied if present.
func UnpackAsar(archive, dir string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()

	// Layout: pickle{uint32 headerSize} then pickle{uint32 len, string json}.
	var pre [16]byte
	if _, err := io.ReadFull(f, pre[:]); err != nil {
		return err
	}
	if binary.LittleEndian.Uint32(pre[0:]) != 4 {
		return errors.New("not an asar archive")
	}
	headerSize := int64(binary.LittleEndian.Uint32(pre[4:]))
	jsonLen := int64(binary.LittleEndian.Uint32(pre[12:]))
	raw := make([]byte, jsonLen)
	if _, err := io.ReadFull(f, raw); err != nil {
		return err
	}
	var root asarEntry
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("asar header: %w", err)
	}
	base := 8 + headerSize
	unpackedDir := archive + ".unpacked"

	var walk func(e *asarEntry, rel string) error
	walk = func(e *asarEntry, rel string) error {
		target := filepath.Join(dir, rel)
		if !strings.HasPrefix(target, filepath.Clean(dir)) {
			return fmt.Errorf("asar entry escapes destination: %s", rel)
		}
		switch {
		case e.Files != nil:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			for name, child := range e.Files {
				if err := walk(child, filepath.Join(rel, name)); err != nil {
					return err
				}
			}
		case e.Link != "":
			return nil
		case e.Unpacked:
			src := filepath.Join(unpackedDir, rel)
			if _, err := os.Stat(src); err == nil {
				return CopyFile(src, target)
			}
		default:
			off, err := strconv.ParseInt(e.Offset, 10, 64)
			if err != nil {
				return fmt.Errorf("bad offset for %s", rel)
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, err = io.Copy(out, io.NewSectionReader(f, base+off, e.Size))
			if cerr := out.Close(); err == nil {
				err = cerr
			}
			return err
		}
		return nil
	}
	return walk(&root, "")
}
