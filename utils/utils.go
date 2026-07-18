package utils

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
)

const FastItoaTableLength = 50 // [0,49], [0, -49]

var (
	FastItoaTablePos = [FastItoaTableLength]string{}
	FastItoaTableNeg = [FastItoaTableLength]string{}
)

func init() {
	for i := range FastItoaTableLength {
		FastItoaTablePos[i] = strconv.Itoa(i)
		FastItoaTableNeg[i] = strconv.Itoa(-i)
	}
}

func FastItoa[T int | uint](i T) string {
	if i >= 0 {
		return FastItoaTablePos[i]
	} else {
		return FastItoaTableNeg[i]
	}
}

func FastItoa4(i1 int, u1 uint, i2 int, u2 uint) (s1, s2, s3, s4 string) {
	return FastItoa(i1), FastItoa(u1), FastItoa(i2), FastItoa(u2)
}

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
