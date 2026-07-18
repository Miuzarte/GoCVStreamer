package weapons

import (
	"fmt"
	"slices"

	"github.com/Miuzarte/GoCVStreamer/utils"
	"github.com/Miuzarte/GoCVStreamer/weapon"
	"gocv.io/x/gocv"
)

type Weapons []*weapon.Weapon

func (ws *Weapons) Append(path string, createMask bool, flag gocv.IMReadFlag) (err error) {
	w, err := weapon.New(path, createMask, flag)
	if err != nil {
		return err
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
	return slices.IndexFunc(*ws, func(w *weapon.Weapon) bool {
		return path == w.Path
	})
}

func (ws *Weapons) IndexByName(name string) int {
	return slices.IndexFunc(*ws, func(w *weapon.Weapon) bool {
		return name == w.Name
	})
}

func (ws *Weapons) ReadFrom(dir string, depth int, suffix string, withoutPrefix string, createMask bool, flag gocv.IMReadFlag) error {
	depth = max(depth, 1)
	paths, err := utils.WalkDir(dir, depth, suffix, withoutPrefix)
	if err != nil {
		return fmt.Errorf("failed to walk %s: %w", suffix, err)
	}

	if cap(*ws) < len(paths) {
		newWs := make(Weapons, len(*ws), len(paths))
		copy(newWs, *ws)
		*ws = newWs
	}
	for _, path := range paths {
		err = ws.Append(path, createMask, flag)
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
