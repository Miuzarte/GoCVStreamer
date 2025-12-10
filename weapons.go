package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/Miuzarte/GoCVStreamer/template"
)

const CREATE_MASK = false

type WeaponMode int

func (tm WeaponMode) String() string {
	switch tm {
	case WEAPON_MODE_FULL_AUTO:
		return "FullAuto"
	case WEAPON_MODE_SEMI_AUTO:
		return "SemiAuto"
	default:
		return fmt.Sprintf("unexpected value of weapon mode: %d", tm)
	}
}

const (
	WEAPON_MODE_FULL_AUTO WeaponMode = iota + 1
	WEAPON_MODE_SEMI_AUTO
)

type Weapon struct {
	Name      string
	Mode      WeaponMode
	SpeedAcog string
	Speed1x   string

	template.Template
}

type Weapons []*Weapon

func (ws *Weapons) ReadFrom(dir string, depth int, suffix string, withoutPrefix string) error {
	depth = max(depth, 1)
	paths, err := walkDir(dir, depth, suffix, withoutPrefix)
	if err != nil {
		return fmt.Errorf("failed to walk %s: %w", suffix, err)
	}

	*ws = make(Weapons, 0, len(paths))
	for i, path := range paths {
		w := &Weapon{}

		base := filepath.Base(path) // "{FA_13_13}9x19VSN.png"
		dotI := strings.LastIndexByte(base, '.')
		if dotI < 0 {
			return fmt.Errorf("invalid file name: %s", base)
		}
		filename := base[:dotI] // "{FA_13_13}9x19VSN"
		bracesL, bracesR := strings.IndexByte(filename, '{'), strings.IndexByte(filename, '}')
		if bracesL < 0 || bracesR < 0 {
			return fmt.Errorf("invalid file params: %s", base)
		}
		params := strings.Split(filename[bracesL+1:bracesR], "_") // ["FA", "13", "13"]
		if len(params) != 3 {
			return fmt.Errorf("invalid file params: %s", params)
		}
		switch params[0] {
		case "FA":
			w.Mode = WEAPON_MODE_FULL_AUTO
		case "SA":
			w.Mode = WEAPON_MODE_SEMI_AUTO
		default:
			return fmt.Errorf("invalid file params mode: %s", params[0])
		}

		w.Name = filename[bracesR+1:]
		w.SpeedAcog = params[1]
		w.Speed1x = params[2]

		err = w.Template.IMReadFrom(path, CREATE_MASK)
		if err != nil {
			return fmt.Errorf("weapon [%d]%s failed to IMRead: %w", i, w.Name, err)
		}

		*ws = append(*ws, w)
	}

	return nil
}

func (ws *Weapons) MinMaxIndex() (min, max int) {
	var maxMinVal, maxMaxVal float32
	for i, w := range *ws {
		if w.Template.MinVal > maxMinVal {
			min = i
		}
		if w.Template.MaxVal > maxMaxVal {
			max = i
		}
	}
	return
}

func (ws *Weapons) Close() (err error) {
	for _, w := range *ws {
		err = w.Template.Close()
		if err != nil {
			return
		}
	}
	return
}

func walkDir(dir string, depth int, suffix string, withoutPrefix string) (paths []string, err error) {
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
