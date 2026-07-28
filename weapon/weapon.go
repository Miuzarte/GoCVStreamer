package weapon

import (
	"fmt"
	"io"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Miuzarte/GoCVStreamer/template"
	"github.com/rs/zerolog/log"
	"gocv.io/x/gocv"
)

type Slot int

const (
	SLOT_UNDEFINED Slot = 0
	SLOT_PRIMARY   Slot = 1 << 0
	SLOT_SECONDARY Slot = 1 << 1
	SLOT_MIX       Slot = SLOT_PRIMARY | SLOT_SECONDARY
)

func (s Slot) Has(other Slot) bool {
	return s&other != 0
}

func (s Slot) Is(other Slot) bool {
	return s == other
}

func (s Slot) String() string {
	switch s {
	case SLOT_PRIMARY:
		return "Primary"
	case SLOT_SECONDARY:
		return "Secondary"
	case SLOT_MIX:
		return "Mix"
	default:
		return fmt.Sprintf("unexpected value of weapon slot: %d", s)
	}
}

func (s Slot) Opposite() Slot {
	hasPrimary := s.Has(SLOT_PRIMARY)
	hasSecondary := s.Has(SLOT_SECONDARY)
	if hasPrimary && hasSecondary {
		return SLOT_UNDEFINED
	}
	if hasPrimary {
		return SLOT_SECONDARY
	}
	if hasSecondary {
		return SLOT_PRIMARY
	}
	return SLOT_UNDEFINED
}

type Type int

const (
	TYPE_UNDEFINED = 0
	TYPE_FULL_AUTO = 1 << 0 // 全自动
	TYPE_SEMI_AUTO = 1 << 1 // 半自动
	// 都存在 (如独头霰弹 ACS12(FA) / BOSG.12.2(SA))
	TYPE_MIX = TYPE_FULL_AUTO | TYPE_SEMI_AUTO
)

func (t Type) Has(other Type) bool {
	return t&other != 0
}

func (t Type) Is(other Type) bool {
	return t == other
}

func (t Type) ToString(short bool) string {
	switch t {
	case TYPE_FULL_AUTO:
		if short {
			return "FA"
		} else {
			return "Full Auto"
		}
	case TYPE_SEMI_AUTO:
		if short {
			return "SA"
		} else {
			return "Semi Auto"
		}
	case TYPE_MIX:
		if short {
			return "MIX"
		} else {
			return "Mix"
		}
	default:
		return fmt.Sprintf("unexpected value of weapon type: %d", t)
	}
}

func (t Type) String() string {
	return t.ToString(false)
}

type Class int

const (
	_ Class = iota

	// (主) (全自动) AR  突击步枪
	CLASS_ASSAULT_RIFLE

	// (主)          GG  装备
	CLASS_GADGET

	// (副) (半自动) HC  手持加农炮
	CLASS_HAND_CANNON

	// (副) (半自动) HG  手枪
	CLASS_HANDGUN

	// (主) (全自动) LMG 轻机枪
	CLASS_LIGHT_MACHINE_GUN

	// (副) (全自动) MP  自动手枪
	CLASS_MACHINE_PISTOL

	// (主) (半自动) MR  射手步枪
	CLASS_MARKSMAN_RIFLE

	// (副) (半自动) RV  左轮手枪
	CLASS_REVOLVER

	// (主/副) (半自动) SG 霰弹枪
	CLASS_SHOTGUN

	// (主) (全自动) SMG 冲锋枪
	CLASS_SUBMACHINE_GUN

	// (主) (半自动) SR  狙击步枪
	CLASS_SNIPER_RIFLE

	// (主) (全/半)  SSG 使用独头弹的霰弹枪
	CLASS_SLUG_SHOTGUN
)

type ClassDetail struct {
	Slot  Slot
	Type  Type
	Class Class

	ShortName string
	FullName  string

	// = 0, 让 lua 方决定
	Offset int
}

var classDetails = [...]ClassDetail{
	0: {},

	CLASS_ASSAULT_RIFLE: {
		SLOT_PRIMARY,
		TYPE_FULL_AUTO,
		CLASS_ASSAULT_RIFLE,

		"AR",
		"Assault Rifle",

		0,
	},

	CLASS_GADGET: {
		SLOT_PRIMARY, // 暂时没有放在副手的装备
		TYPE_UNDEFINED,
		CLASS_GADGET,

		"GG",
		"Gadget",

		0,
	},

	CLASS_HAND_CANNON: {
		SLOT_SECONDARY,
		TYPE_SEMI_AUTO,
		CLASS_HAND_CANNON,

		"HC",
		"Hand Cannon",

		0,
	},

	CLASS_HANDGUN: {
		SLOT_SECONDARY,
		TYPE_SEMI_AUTO,
		CLASS_HANDGUN,

		"HG",
		"Handgun",

		0,
	},

	CLASS_LIGHT_MACHINE_GUN: {
		SLOT_PRIMARY,
		TYPE_FULL_AUTO,
		CLASS_LIGHT_MACHINE_GUN,

		"LMG",
		"Light Machine Gun",

		0,
	},

	CLASS_MACHINE_PISTOL: {
		SLOT_SECONDARY,
		TYPE_FULL_AUTO,
		CLASS_MACHINE_PISTOL,

		"MP",
		"Machine Pistol",

		0,
	},

	CLASS_MARKSMAN_RIFLE: {
		SLOT_PRIMARY,
		TYPE_SEMI_AUTO,
		CLASS_MARKSMAN_RIFLE,

		"MR",
		"Marksman Rifle",

		0,
	},

	CLASS_REVOLVER: {
		SLOT_SECONDARY,
		TYPE_SEMI_AUTO,
		CLASS_REVOLVER,

		"RV",
		"Revolver",

		0,
	},

	CLASS_SHOTGUN: {
		SLOT_MIX,
		TYPE_SEMI_AUTO,
		CLASS_SHOTGUN,

		"SG",
		"Shotgun",

		0,
	},

	CLASS_SUBMACHINE_GUN: {
		SLOT_PRIMARY,
		TYPE_FULL_AUTO,
		CLASS_SUBMACHINE_GUN,

		"SMG",
		"Submachine Gun",

		0,
	},

	CLASS_SNIPER_RIFLE: {
		SLOT_PRIMARY,
		TYPE_SEMI_AUTO,
		CLASS_SNIPER_RIFLE,

		"SR",
		"Sniper Rifle",

		0,
	},

	CLASS_SLUG_SHOTGUN: {
		SLOT_PRIMARY,
		TYPE_MIX,
		CLASS_SLUG_SHOTGUN,

		"SSG",
		"Slug Shotgun",

		0,
	},
}

func (c Class) Detail() *ClassDetail {
	// let it panic if oob
	return &classDetails[c]
}

func (c Class) ToString(short bool) string {
	if short {
		return c.Detail().ShortName
	} else {
		return c.Detail().FullName
	}
}

func (c Class) String() string {
	return c.ToString(false)
}

func ParseClass(className string) Class {
	// 先判断短名字, 一般用这个
	for _, wcd := range classDetails {
		if className == wcd.ShortName {
			return wcd.Class
		}
	}
	for _, wcd := range classDetails {
		if className == wcd.FullName {
			return wcd.Class
		}
	}
	return 0
}

const (
	PARAM_CLASS = iota
	PARAM_SPEED_MAIN
	PARAM_SPEED_ALT
	PARAM_COUNT
)

const (
	SPEED_ALTERNATIVE_RATIO = 0.7
	SPEED_SIGN_AUTO         = "--" // *0.7
	SPEED_SIGN_COPY         = "==" // *1
)

type Weapon struct {
	Path  string
	Name  string
	Class Class
	// Type WeaponType
	SpeedMain     float64
	SpeedMainInt  int
	SpeedMainFrac uint
	SpeedAlt      float64
	SpeedAltInt   int
	SpeedAltFrac  uint

	template.Template
}

func (w *Weapon) String() string {
	return fmt.Sprintf(
		"{%s_%02d.%d_%02d.%d} %s",
		// w.Type.string(true),
		w.Class.ToString(true),
		w.SpeedMainInt, w.SpeedMainFrac,
		w.SpeedAltInt, w.SpeedAltFrac,
		w.Name,
	)
}

func New(path string, createMask bool, flag gocv.IMReadFlag) (*Weapon, error) {
	w := new(Weapon{})
	err := w.DecodeFrom(path, createMask, flag)
	if err != nil {
		return nil, err
	}
	return w, nil
}

func (w *Weapon) DecodeFrom(path string, createMask bool, flag gocv.IMReadFlag) error {
	name, params, err := ParseFileName(path)
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
	// [PARAM_CLASS]
	w.Class = ParseClass(params[PARAM_CLASS])
	// w.Type = w.Class.Detail().Type

	// [PARAM_SPEED_MAIN]
	w.SpeedMain, err = strconv.ParseFloat(params[PARAM_SPEED_MAIN], 64)
	if err != nil {
		return err
	}
	integer, fraction := math.Modf(w.SpeedMain)
	w.SpeedMainInt, w.SpeedMainFrac = int(integer), uint(math.Round(fraction*10))

	// [PARAM_SPEED_ALT]
	switch params[PARAM_SPEED_ALT] {
	case SPEED_SIGN_AUTO:
		w.SpeedAlt = w.SpeedMain * SPEED_ALTERNATIVE_RATIO
	case SPEED_SIGN_COPY:
		w.SpeedAlt = w.SpeedMain
	default:
		w.SpeedAlt, err = strconv.ParseFloat(params[PARAM_SPEED_ALT], 64)
		if err != nil {
			return err
		}
	}
	integer, fraction = math.Modf(w.SpeedAlt)
	w.SpeedAltInt, w.SpeedAltFrac = int(integer), uint(math.Round(fraction*10))

	err = w.Template.IMReadFrom(path, createMask, flag)
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

func (w *Weapon) DisplaySpeed(
	sw interface {
		io.StringWriter
		io.Writer
	}, debug bool,
) {
	if w == nil {
		sw.WriteString("No weapon")
		return
	}

	spMain, spMainF, spAlt, spAltF := w.GetAllSpeeds(debug)
	typ := w.Class.Detail().Type

	fmt.Fprintf(sw, "\n%s\n", w.Name)
	if typ.Has(TYPE_FULL_AUTO) {
		fmt.Fprintf(sw, "FA: %s / %s\n",
			formatSpeed(spMain, int(spMainF)),
			formatSpeed(spAlt, int(spAltF)))
	}
	if typ.Has(TYPE_SEMI_AUTO) {
		fmt.Fprintf(sw, "SA: %s / %s",
			formatSpeed(spMain, int(spMainF)),
			formatSpeed(spAlt, int(spAltF)))
	}
	return
}

func formatSpeed(intPart, frac int) string {
	if intPart < 0 {
		return "off"
	}
	return fmt.Sprintf("%d.%d", intPart, frac)
}

func ParseFileName(path string) (name string, params [PARAM_COUNT]string, err error) {
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
	if len(ps) != PARAM_COUNT {
		err = fmt.Errorf("invalid weapon params: %s", params)
		return
	}
	copy(params[:], ps)
	name = strings.TrimSpace(filename[bracesR+1:]) // "9x19VSN"
	return
}
