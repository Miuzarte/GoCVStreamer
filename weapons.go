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

type WeaponSlot int

const (
	WEAPON_SLOT_UNDEFINED = 0
	WEAPON_SLOT_PRIMARY   = 1 << 0
	WEAPON_SLOT_SECONDARY = 1 << 1
	WEAPON_SLOT_MIX       = WEAPON_SLOT_PRIMARY | WEAPON_SLOT_SECONDARY
)

func (ws WeaponSlot) Has(other WeaponSlot) bool {
	return ws&other != 0
}

func (ws WeaponSlot) Is(other WeaponSlot) bool {
	return ws == other
}

func (ws WeaponSlot) String() string {
	switch ws {
	case WEAPON_SLOT_PRIMARY:
		return "Primary"
	case WEAPON_SLOT_SECONDARY:
		return "Secondary"
	case WEAPON_SLOT_MIX:
		return "Mix"
	default:
		return fmt.Sprintf("unexpected value of weapon slot: %d", ws)
	}
}

type WeaponType int

const (
	WEAPON_TYPE_UNDEFINED = 0
	WEAPON_TYPE_FULL_AUTO = 1 << 0 // 全自动
	WEAPON_TYPE_SEMI_AUTO = 1 << 1 // 半自动
	// 都存在 (如独头霰弹 ACS12(FA) / BOSG.12.2(SA))
	WEAPON_TYPE_MIX = WEAPON_TYPE_FULL_AUTO | WEAPON_TYPE_SEMI_AUTO
)

func (wt WeaponType) Has(other WeaponType) bool {
	return wt&other != 0
}

func (wt WeaponType) Is(other WeaponType) bool {
	return wt == other
}

func (wt WeaponType) string(short bool) string {
	switch wt {
	case WEAPON_TYPE_FULL_AUTO:
		if short {
			return "FA"
		} else {
			return "Full Auto"
		}
	case WEAPON_TYPE_SEMI_AUTO:
		if short {
			return "SA"
		} else {
			return "Semi Auto"
		}
	case WEAPON_TYPE_MIX:
		if short {
			return "MIX"
		} else {
			return "Mix"
		}
	default:
		return fmt.Sprintf("unexpected value of weapon type: %d", wt)
	}
}

func (wt WeaponType) String() string {
	return wt.string(false)
}

type WeaponClass int

const (
	_ WeaponClass = iota

	// (主) (全自动) AR  突击步枪
	WEAPON_CLASS_ASSAULT_RIFLE

	// (主)          GG  装备
	WEAPON_CLASS_GADGET

	// (副) (半自动) HC  手持加农炮
	WEAPON_CLASS_HAND_CANNON

	// (副) (半自动) HG  手枪
	WEAPON_CLASS_HANDGUN

	// (主) (全自动) LMG 轻机枪
	WEAPON_CLASS_LIGHT_MACHINE_GUN

	// (副) (全自动) MP  自动手枪
	WEAPON_CLASS_MACHINE_PISTOL

	// (主) (半自动) MR  射手步枪
	WEAPON_CLASS_MARKSMAN_RIFLE

	// (副) (半自动) RV  左轮手枪
	WEAPON_CLASS_REVOLVER

	// (主/副) (半自动) SG 霰弹枪
	WEAPON_CLASS_SHOTGUN

	// (主) (全自动) SMG 冲锋枪
	WEAPON_CLASS_SUBMACHINE_GUN

	// (主) (半自动) SR  狙击步枪
	WEAPON_CLASS_SNIPER_RIFLE

	// (主) (全/半)  SSG 使用独头弹的霰弹枪
	WEAPON_CLASS_SLUG_SHOTGUN
)

type WeaponClassDetail struct {
	Slot  WeaponSlot
	Type  WeaponType
	Class WeaponClass

	ShortName string
	FullName  string

	// = 0, 让 lua 方决定
	Offset int
}

var weaponClassDetails = [...]WeaponClassDetail{
	0: {},

	WEAPON_CLASS_ASSAULT_RIFLE: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_FULL_AUTO,
		WEAPON_CLASS_ASSAULT_RIFLE,

		"AR",
		"Assault Rifle",

		0,
	},

	WEAPON_CLASS_GADGET: {
		WEAPON_SLOT_PRIMARY, // 暂时没有放在副手的装备
		WEAPON_TYPE_UNDEFINED,
		WEAPON_CLASS_GADGET,

		"GG",
		"Gadget",

		0,
	},

	WEAPON_CLASS_HAND_CANNON: {
		WEAPON_SLOT_SECONDARY,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_HAND_CANNON,

		"HC",
		"Hand Cannon",

		0,
	},

	WEAPON_CLASS_HANDGUN: {
		WEAPON_SLOT_SECONDARY,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_HANDGUN,

		"HG",
		"Handgun",

		0,
	},

	WEAPON_CLASS_LIGHT_MACHINE_GUN: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_FULL_AUTO,
		WEAPON_CLASS_LIGHT_MACHINE_GUN,

		"LMG",
		"Light Machine Gun",

		0,
	},

	WEAPON_CLASS_MACHINE_PISTOL: {
		WEAPON_SLOT_SECONDARY,
		WEAPON_TYPE_FULL_AUTO,
		WEAPON_CLASS_MACHINE_PISTOL,

		"MP",
		"Machine Pistol",

		0,
	},

	WEAPON_CLASS_MARKSMAN_RIFLE: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_MARKSMAN_RIFLE,

		"MR",
		"Marksman Rifle",

		0,
	},

	WEAPON_CLASS_REVOLVER: {
		WEAPON_SLOT_SECONDARY,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_REVOLVER,

		"RV",
		"Revolver",

		0,
	},

	WEAPON_CLASS_SHOTGUN: {
		WEAPON_SLOT_MIX,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_SHOTGUN,

		"SG",
		"Shotgun",

		0,
	},

	WEAPON_CLASS_SUBMACHINE_GUN: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_FULL_AUTO,
		WEAPON_CLASS_SUBMACHINE_GUN,

		"SMG",
		"Submachine Gun",

		0,
	},

	WEAPON_CLASS_SNIPER_RIFLE: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_SEMI_AUTO,
		WEAPON_CLASS_SNIPER_RIFLE,

		"SR",
		"Sniper Rifle",

		0,
	},

	WEAPON_CLASS_SLUG_SHOTGUN: {
		WEAPON_SLOT_PRIMARY,
		WEAPON_TYPE_MIX,
		WEAPON_CLASS_SLUG_SHOTGUN,

		"SSG",
		"Slug Shotgun",

		0,
	},
}

func (wc WeaponClass) Detail() *WeaponClassDetail {
	// let it panic if oob
	return &weaponClassDetails[wc]
}

func (wc WeaponClass) string(short bool) string {
	if short {
		return wc.Detail().ShortName
	} else {
		return wc.Detail().FullName
	}
}

func (wc WeaponClass) String() string {
	return wc.string(false)
}

func ParseWeaponClass(className string) WeaponClass {
	// 先判断短名字, 一般用这个
	for _, wcd := range weaponClassDetails {
		if className == wcd.ShortName {
			return wcd.Class
		}
	}
	for _, wcd := range weaponClassDetails {
		if className == wcd.FullName {
			return wcd.Class
		}
	}
	return 0
}

const (
	WEAPON_PARAM_CLASS = iota
	WEAPON_PARAM_SPEED_MAIN
	WEAPON_PARAM_SPEED_ALT
	WEAPON_PARAM_COUNT
)

const (
	SPEED_ALTERNATIVE_RATIO = 0.7
	SPEED_SIGN_AUTO         = "--" // *0.7
	SPEED_SIGN_COPY         = "==" // *1
)

type Weapon struct {
	Path  string
	Name  string
	Class WeaponClass
	// Type WeaponType
	SpeedMain     float64
	SpeedMainInt  int
	SpeedMainFrac uint
	SpeedAlt      float64
	SpeedAltInt   int
	SpeedAltFrac  uint

	template.Template
	luaBuf bytes.Buffer
}

func (w *Weapon) String() string {
	return fmt.Sprintf(
		"{%s_%02d.%d_%02d.%d} %s",
		// w.Type.string(true),
		w.Class.string(true),
		w.SpeedMainInt, w.SpeedMainFrac,
		w.SpeedAltInt, w.SpeedAltFrac,
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
			log.Warn().
				Str("oldName", w.Name).
				Str("newName", name).
				Msg("weapon name changed")
		}
	}

	w.Path = path
	w.Name = name
	// [WEAPON_PARAM_CLASS]
	w.Class = ParseWeaponClass(params[WEAPON_PARAM_CLASS])
	// w.Type = w.Class.Detail().Type

	// [WEAPON_PARAM_SPEED_MAIN]
	w.SpeedMain, err = strconv.ParseFloat(params[WEAPON_PARAM_SPEED_MAIN], 64)
	if err != nil {
		return err
	}
	integer, fraction := math.Modf(w.SpeedMain)
	w.SpeedMainInt, w.SpeedMainFrac = int(integer), uint(math.Round(fraction*10))

	// [WEAPON_PARAM_SPEED_ALT]
	switch params[WEAPON_PARAM_SPEED_ALT] {
	case SPEED_SIGN_AUTO:
		w.SpeedAlt = w.SpeedMain * SPEED_ALTERNATIVE_RATIO
	case SPEED_SIGN_COPY:
		w.SpeedAlt = w.SpeedMain
	default:
		w.SpeedAlt, err = strconv.ParseFloat(params[WEAPON_PARAM_SPEED_ALT], 64)
		if err != nil {
			return err
		}
	}
	integer, fraction = math.Modf(w.SpeedAlt)
	w.SpeedAltInt, w.SpeedAltFrac = int(integer), uint(math.Round(fraction*10))

	err = w.Template.IMReadFrom(path, CREATE_MASK, MATCHING_MODE)
	if err != nil {
		return fmt.Errorf("weapon %s failed to IMRead: %w", w.Name, err)
	}
	return nil
}

func (w *Weapon) SpeedMainWOffset() (int, uint) {
	if w.SpeedMain == 0 {
		return 0, 0
	}
	return w.SpeedMainInt + w.Class.Detail().Offset, w.SpeedMainFrac
}

func (w *Weapon) SpeedAlternativeWOffset() (int, uint) {
	if w.SpeedAlt == 0 {
		return 0, 0
	}
	return w.SpeedAltInt + w.Class.Detail().Offset, w.SpeedAltFrac
}

func (w *Weapon) GetAllSpeeds(orig bool) (speedMain int, speedMainF uint, speedAlt int, speedAltF uint) {
	if !orig {
		speedMain, speedMainF = w.SpeedMainWOffset()
		speedAlt, speedAltF = w.SpeedAlternativeWOffset()
	} else {
		// debugging, use orig
		speedMain, speedMainF = w.SpeedMainInt, w.SpeedMainFrac
		speedAlt, speedAltF = w.SpeedAltInt, w.SpeedAltFrac
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

func (w *Weapon) Lua(orig bool) []byte {
	if w == nil {
		return []byte(DEFAULT_CONTENT)
	}

	w.luaBuf.Reset()

	speedMain, speedMainF, speedAlt, speedAltF := FastItoa4(w.GetAllSpeeds(orig))

	d := w.Class.Detail()
	if d.Type.Has(WEAPON_TYPE_FULL_AUTO) {
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
	} else {
		w.luaBuf.WriteString(DEFAULT_CONTENT_FULL_AUTO)
	}
	if d.Type.Has(WEAPON_TYPE_SEMI_AUTO) {
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
	} else {
		w.luaBuf.WriteString(DEFAULT_CONTENT_SEMI_AUTO)
	}

	return w.luaBuf.Bytes()
}

type Weapons []*Weapon

func parseFileName(path string) (name string, params [WEAPON_PARAM_COUNT]string, err error) {
	base := filepath.Base(path) // "{SMG_9_==} 9x19VSN.png"
	dotI := strings.LastIndexByte(base, '.')
	if dotI < 0 {
		err = fmt.Errorf("invalid file name: %s", base)
		return
	}

	filename := base[:dotI] // "{SMG_9_==} 9x19VSN"

	bracesL, bracesR := strings.IndexByte(filename, '{'), strings.IndexByte(filename, '}')
	if bracesL < 0 || bracesR < 0 {
		err = fmt.Errorf("invalid file params: %s", base)
		return
	}

	p := filename[bracesL+1 : bracesR] // SMG_9_==
	ps := strings.Split(p, "_")        // ["SMG", "9", "=="]
	if len(ps) != WEAPON_PARAM_COUNT {
		err = fmt.Errorf("invalid weapon params: %s", params)
		return
	}
	copy(params[:], ps)
	name = strings.TrimSpace(filename[bracesR+1:]) // "9x19VSN"
	return
}

func (ws *Weapons) Append(path string) (err error) {
	w := new(Weapon{})

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
