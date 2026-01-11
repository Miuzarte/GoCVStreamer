package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/Miuzarte/GoCVStreamer/template"
)

const CREATE_MASK = false

type WeaponType int

const (
	WEAPON_TYPE_MACHINE_PISTOL WeaponType = iota + 1
	WEAPON_TYPE_SUBMACHINE_GUN
	WEAPON_TYPE_ASSAULT_RIFLE
	WEAPON_TYPE_LIGHT_MACHINE_GUN
	WEAPON_TYPE_SEMI_AUTO
)

func (wt WeaponType) string(short bool) string {
	switch wt {
	case WEAPON_TYPE_MACHINE_PISTOL:
		if short {
			return "MP"
		} else {
			return "MachinePistol"
		}
	case WEAPON_TYPE_SUBMACHINE_GUN:
		if short {
			return "SG"
		} else {
			return "SubmachineGun"
		}
	case WEAPON_TYPE_ASSAULT_RIFLE:
		if short {
			return "AR"
		} else {
			return "AssaultRifle"
		}
	case WEAPON_TYPE_LIGHT_MACHINE_GUN:
		if short {
			return "MG"
		} else {
			return "LightMachineGun"
		}
	case WEAPON_TYPE_SEMI_AUTO:
		if short {
			return "SA"
		} else {
			return "SemiAuto"
		}
	default:
		return fmt.Sprintf("unexpected value of weapon type: %d", wt)
	}
}

func (wt WeaponType) String() string {
	return wt.string(false)
}

var OffsetTable = [...]int{
	0:                             0,
	WEAPON_TYPE_MACHINE_PISTOL:    -2,
	WEAPON_TYPE_SUBMACHINE_GUN:    -2,
	WEAPON_TYPE_ASSAULT_RIFLE:     -2,
	WEAPON_TYPE_LIGHT_MACHINE_GUN: -1,
	WEAPON_TYPE_SEMI_AUTO:         -2,
}

func (wt WeaponType) SpeedOffset() int {
	return OffsetTable[wt]
}

func ParseWeaponType(typ string) (WeaponType, error) {
	switch typ {
	case "MP":
		return WEAPON_TYPE_MACHINE_PISTOL, nil
	case "SG":
		return WEAPON_TYPE_SUBMACHINE_GUN, nil
	case "AR":
		return WEAPON_TYPE_ASSAULT_RIFLE, nil
	case "LMG", "MG":
		return WEAPON_TYPE_LIGHT_MACHINE_GUN, nil
	case "SA":
		return WEAPON_TYPE_SEMI_AUTO, nil
	default:
		return 0, fmt.Errorf("invalid weapon type: %s", typ)
	}
}

type WeaponMode int

const (
	WEAPON_MODE_FULL_AUTO WeaponMode = iota + 1
	WEAPON_MODE_SEMI_AUTO
)

func (wm WeaponMode) string(short bool) string {
	switch wm {
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
		return fmt.Sprintf("unexpected value of weapon mode: %d", wm)
	}
}

func (wm WeaponMode) String() string {
	return wm.string(false)
}

func ParseWeaponMode(mode string) (WeaponMode, error) {
	switch mode {
	case "FA":
		return WEAPON_MODE_FULL_AUTO, nil
	case "SA":
		return WEAPON_MODE_SEMI_AUTO, nil
	default:
		return 0, fmt.Errorf("invalid weapon mode: %s", mode)
	}
}

const WEAPON_PARAMS_NUM = 4

const (
	SPEED_ALTERNATIVE_RATIO = 0.7
	SPEED_SIGN_AUTO         = "--"
	SPEED_SIGN_COPY         = "=="
)

type Weapon struct {
	Path                 string
	Name                 string
	Type                 WeaponType
	Mode                 WeaponMode
	SpeedMain            float64
	SpeedMainInt         int
	SpeedMainFrac        uint
	SpeedAlternative     float64
	SpeedAlternativeInt  int
	SpeedAlternativeFrac uint

	template.Template
	luaBuf bytes.Buffer
}

func (w *Weapon) String() string {
	return fmt.Sprintf(
		"{%s_%s_%02d.%d_%02d.%d} %s",
		w.Mode.string(true), w.Type.string(true),
		w.SpeedMainInt, w.SpeedMainFrac,
		w.SpeedAlternativeInt, w.SpeedAlternativeFrac,
		w.Name,
	)
}

func (w *Weapon) DecodeFrom(path string) error {
	name, params, err := parseFileName(path)
	if err != nil {
		return err
	}

	if w.Name != "" {
		// overwriting
		if name != w.Name {
			// wried
			log.Warnf("weapon %q name was changed to %q", w.Name, name)
		}
	}

	w.Path = path
	w.Name = name
	w.Mode, err = ParseWeaponMode(params[0])
	if err != nil {
		return err
	}
	w.Type, err = ParseWeaponType(params[1])
	if err != nil {
		return err
	}

	w.SpeedMain, err = strconv.ParseFloat(params[2], 64)
	if err != nil {
		return err
	}
	integer, fraction := math.Modf(w.SpeedMain)
	w.SpeedMainInt, w.SpeedMainFrac = int(integer), uint(math.Round(fraction*10))

	switch params[3] {
	case SPEED_SIGN_AUTO:
		w.SpeedAlternative = w.SpeedMain * SPEED_ALTERNATIVE_RATIO
	case SPEED_SIGN_COPY:
		w.SpeedAlternative = w.SpeedMain
	default:
		w.SpeedAlternative, err = strconv.ParseFloat(params[3], 64)
		if err != nil {
			return err
		}
	}
	integer, fraction = math.Modf(w.SpeedAlternative)
	w.SpeedAlternativeInt, w.SpeedAlternativeFrac = int(integer), uint(math.Round(fraction*10))

	err = w.Template.IMReadFrom(path, CREATE_MASK)
	if err != nil {
		return fmt.Errorf("weapon %s failed to IMRead: %w", w.Name, err)
	}
	return nil
}

func (w *Weapon) SpeedMainWOffset() (int, uint) {
	if w.SpeedMain == 0 {
		return 0, 0
	}
	return w.SpeedMainInt + w.Type.SpeedOffset(), w.SpeedMainFrac
}

func (w *Weapon) SpeedAlternativeWOffset() (int, uint) {
	if w.SpeedAlternative == 0 {
		return 0, 0
	}
	return w.SpeedAlternativeInt + w.Type.SpeedOffset(), w.SpeedAlternativeFrac
}

func (w *Weapon) GetAllSpeeds(orig bool) (speedMain int, speedMainF uint, speedAlt int, speedAltF uint) {
	if !orig {
		speedMain, speedMainF = w.SpeedMainWOffset()
		speedAlt, speedAltF = w.SpeedAlternativeWOffset()
	} else {
		// debugging, use orig
		speedMain, speedMainF = w.SpeedMainInt, w.SpeedMainFrac
		speedAlt, speedAltF = w.SpeedAlternativeInt, w.SpeedAlternativeFrac
	}
	return
}

const DEFAULT_CONTENT_FULL_AUTO = "FAM=0" + "\n" +
	"FAMF=0" + "\n" +
	"FAA=0" + "\n" +
	"FAAF=0" + "\n"

const DEFAULT_CONTENT_SEMI_AUTO = "SAM=-1" + "\n" +
	"SAMF=0" + "\n" +
	"SAA=-1" + "\n" +
	"SAAF=0" + "\n"

const DEFAULT_CONTENT = DEFAULT_CONTENT_FULL_AUTO + DEFAULT_CONTENT_SEMI_AUTO

func (w *Weapon) Lua(debugging bool) []byte {
	if w == nil {
		return []byte(DEFAULT_CONTENT)
	}

	w.luaBuf.Reset()

	speedMain, speedMainF, speedAlt, speedAltF := FastItoa4(w.GetAllSpeeds(debugging))

	switch w.Mode {
	case WEAPON_MODE_FULL_AUTO:
		w.luaBuf.WriteString("FAM=")
		w.luaBuf.WriteString(speedMain)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("FAMF=")
		w.luaBuf.WriteString(speedMainF)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("FAA=")
		w.luaBuf.WriteString(speedAlt)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("FAAF=")
		w.luaBuf.WriteString(speedAltF)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString(DEFAULT_CONTENT_SEMI_AUTO)

	case WEAPON_MODE_SEMI_AUTO:
		w.luaBuf.WriteString("SAM=")
		w.luaBuf.WriteString(speedMain)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("SAMF=")
		w.luaBuf.WriteString(speedMainF)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("SAA=")
		w.luaBuf.WriteString(speedAlt)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString("SAAF=")
		w.luaBuf.WriteString(speedAltF)
		w.luaBuf.WriteByte('\n')
		w.luaBuf.WriteString(DEFAULT_CONTENT_FULL_AUTO)

	default:
		log.Panicf("unexpected Weapon.Mode: %d", w.Mode)
	}

	return w.luaBuf.Bytes()
}

type Weapons []*Weapon

func parseFileName(path string) (name string, params [WEAPON_PARAMS_NUM]string, err error) {
	base := filepath.Base(path) // "{FA_SG_13.0_13.0} 9x19VSN.png"
	dotI := strings.LastIndexByte(base, '.')
	if dotI < 0 {
		err = fmt.Errorf("invalid file name: %s", base)
		return
	}

	filename := base[:dotI] // "{FA_SG_13.0_13.0} 9x19VSN"

	bracesL, bracesR := strings.IndexByte(filename, '{'), strings.IndexByte(filename, '}')
	if bracesL < 0 || bracesR < 0 {
		err = fmt.Errorf("invalid file params: %s", base)
		return
	}

	p := filename[bracesL+1 : bracesR] // FA_SG_13.0_13.0
	ps := strings.Split(p, "_")        // ["FA", "SG", "13.0", "13.0"]
	if len(ps) != WEAPON_PARAMS_NUM {
		err = fmt.Errorf("invalid weapon params: %s", params)
		return
	}
	copy(params[:], ps)
	name = strings.TrimSpace(filename[bracesR+1:]) // "9x19VSN"
	return
}

func (ws *Weapons) Append(path string) (err error) {
	w := &Weapon{}

	err = w.DecodeFrom(path)
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
