// Package kits embeds the sbx sandbox kits so the binary can run from anywhere.
package kits

import (
	"embed"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

//go:embed all:dsh
var files embed.FS

// WriteDsh writes the dsh kit to dir, replacing whatever was there.
func WriteDsh(dir string) error {
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return fs.WalkDir(files, "dsh", func(name string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path.Base(name) == ".DS_Store" {
			return nil
		}
		rel, err := filepath.Rel("dsh", filepath.FromSlash(name))
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := files.ReadFile(name)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
