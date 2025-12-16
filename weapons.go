package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Miuzarte/GoCVStreamer/template"
)

const CREATE_MASK = false

type WeaponMode int

func (tm WeaponMode) string(short bool) string {
	switch tm {
	case WEAPON_MODE_FULL_AUTO:
		if short {
			return "FA"
		} else {
			return "FullAuto"
		}
	case WEAPON_MODE_SEMI_AUTO:
		if short {
			return "SA"
		} else {
			return "SemiAuto"
		}
	default:
		return fmt.Sprintf("unexpected value of weapon mode: %d", tm)
	}
}

func (tm WeaponMode) String() string {
	return tm.string(false)
}

const WEAPON_PARAMS_NUM = 3

const (
	WEAPON_MODE_FULL_AUTO WeaponMode = iota + 1
	WEAPON_MODE_SEMI_AUTO
)

type Weapon struct {
	Path           string
	Name           string
	Mode           WeaponMode
	SpeedMain      string
	SpeedSecondary string

	template.Template
}

func (w *Weapon) String() string {
	return fmt.Sprintf("{%s_%s_%s}%s", w.Mode.string(true), w.SpeedMain, w.SpeedSecondary, w.Name)
}

type Weapons []*Weapon

func parseFileName(path string) (name string, params [WEAPON_PARAMS_NUM]string, err error) {
	base := filepath.Base(path) // "{FA_13_13} 9x19VSN.png"
	dotI := strings.LastIndexByte(base, '.')
	if dotI < 0 {
		err = fmt.Errorf("invalid file name: %s", base)
		return
	}

	filename := base[:dotI] // "{FA_13_13} 9x19VSN"

	bracesL, bracesR := strings.IndexByte(filename, '{'), strings.IndexByte(filename, '}')
	if bracesL < 0 || bracesR < 0 {
		err = fmt.Errorf("invalid file params: %s", base)
		return
	}

	name = strings.TrimSpace(filename[bracesR+1:]) // "9x19VSN"

	p := strings.Split(filename[bracesL+1:bracesR], "_") // ["FA", "13", "13"]
	if len(p) != WEAPON_PARAMS_NUM {
		err = fmt.Errorf("invalid file params: %s", params)
		return
	}
	copy(params[:], p)
	return
}

func (ws *Weapons) Append(path string) error {
	name, params, err := parseFileName(path)
	if err != nil {
		return err
	}

	w := &Weapon{}

	switch params[0] {
	case "FA":
		w.Mode = WEAPON_MODE_FULL_AUTO
	case "SA":
		w.Mode = WEAPON_MODE_SEMI_AUTO
	default:
		return fmt.Errorf("invalid file params mode: %s", params[0])
	}

	w.Path = path
	w.Name = name
	w.SpeedMain = params[1]
	w.SpeedSecondary = params[2]

	err = w.Template.IMReadFrom(path, CREATE_MASK)
	if err != nil {
		return fmt.Errorf("weapon %s failed to IMRead: %w", w.Name, err)
	}

	*ws = append(*ws, w)

	return nil
}

func (ws *Weapons) Delete(i int) (err error) {
	w := (*ws)[i]
	err = w.Close()
	if err != nil {
		return
	}
	*ws = append((*ws)[:i], (*ws)[i+1:]...)
	return nil
}

func (ws *Weapons) DeleteByPath(path string) (deleted int, err error) {
	for {
		i := ws.IndexByPath(path)
		if i < 0 {
			return
		}
		err = ws.Delete(i)
		if err != nil {
			return
		}
		deleted++
	}
}

func (ws *Weapons) DeleteByName(name string) (deleted int, err error) {
	for {
		i := ws.IndexByName(name)
		if i < 0 {
			return
		}
		err = ws.Delete(i)
		if err != nil {
			return
		}
		deleted++
	}
}

func (ws *Weapons) IndexByPath(path string) int {
	return slices.IndexFunc(*ws, func(w *Weapon) bool {
		return path == w.Path
	})
}

func (ws *Weapons) IndexByName(name string) int {
	return slices.IndexFunc(*ws, func(w *Weapon) bool {
		return name == w.Name
	})
}

func (ws *Weapons) ReadFrom(dir string, depth int, suffix string, withoutPrefix string) error {
	depth = max(depth, 1)
	paths, err := walkDir(dir, depth, suffix, withoutPrefix)
	if err != nil {
		return fmt.Errorf("failed to walk %s: %w", suffix, err)
	}

	if cap(*ws) < len(paths) {
		newWs := make(Weapons, len(*ws), len(paths))
		copy(newWs, *ws)
		*ws = newWs
	}
	for _, path := range paths {
		err = ws.Append(path)
		if err != nil {
			return err
		}
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
	for i := len(*ws) - 1; i >= 0; i-- {
		err = (*ws)[i].Template.Close()
		if err != nil {
			return
		}
		*ws = (*ws)[:i]
	}
	if len(*ws) != 0 {
		panic("unreachable")
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
