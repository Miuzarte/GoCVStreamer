package utils

import (
	"io/fs"
	"path/filepath"
	"strings"
)

func WalkDir(dir string, depth int, suffix string, withoutPrefix string) (paths []string, err error) {
	dir = filepath.Clean(dir)
	if !strings.HasPrefix(suffix, ".") {
		suffix = "." + suffix
	}

	err = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		relDepth := 0
		if relPath != "." {
			relDepth = strings.Count(relPath, string(filepath.Separator)) + 1
		}
		if relDepth > depth && d.IsDir() {
			return filepath.SkipDir
		}

		if !d.IsDir() && strings.EqualFold(filepath.Ext(path), suffix) && !strings.HasPrefix(filepath.Base(path), withoutPrefix) {
			if relDepth <= depth {
				paths = append(paths, path)
			}
		}

		return nil
	})

	return
}
