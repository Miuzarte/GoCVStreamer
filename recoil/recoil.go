package recoil

import (
	"fmt"
	"io"
	"math/rand/v2"
	"sync"

	"github.com/Miuzarte/GoCVStreamer/keystate"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/mouse"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
	"github.com/rs/zerolog"
)

type Config struct {
	Debugging bool
}

type Engine struct {
	log zerolog.Logger
	mu  sync.RWMutex
	wp  *w.Weapon

	keys    *keystate.Tracker
	clicker *mouse.Clicker
	Alt     bool

	SpeedOffset    int
	HoriJitterBase int
	horiSign       int
	fracCount      int

	debugging bool
}

func New(clicker *mouse.Clicker, cfg Config) *Engine {
	return &Engine{
		log:            logger.New("Engine"),
		keys:           keystate.NewTracker(),
		clicker:        clicker,
		SpeedOffset:    -4,
		HoriJitterBase: 2,
		horiSign:       1,
		debugging:      cfg.Debugging,
	}
}

func (e *Engine) Close() {
	e.clicker.Close()
}

func (e *Engine) SetWeapon(weap *w.Weapon) {
	e.mu.Lock()
	e.wp = weap
	e.mu.Unlock()
}

func (e *Engine) Weapon() *w.Weapon {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.wp
}

func (e *Engine) Tick() {
	shift := keystate.IsDown(keystate.VK_SHIFT)

	if e.keys.Pressed(keystate.VK_OEM_PLUS) {
		if !shift {
			e.SpeedOffset++
			e.log.Info().
				Int("Offset", e.SpeedOffset).
				Send()
		} else {
			e.HoriJitterBase++
			e.log.Info().
				Int("Jitter", e.HoriJitterBase).
				Send()
		}
	}
	if e.keys.Pressed(keystate.VK_OEM_MINUS) {
		if !shift {
			e.SpeedOffset--
			e.log.Info().
				Int("Offset", e.SpeedOffset).
				Send()
		} else {
			e.HoriJitterBase--
			e.log.Info().
				Int("Jitter", e.HoriJitterBase).
				Send()
		}
	}

	if e.keys.Pressed(keystate.VK_INSERT) {
		e.Alt = !e.Alt
		e.log.Info().
			Bool("Alt", e.Alt).
			Send()
	}

	e.fullAutoTick()
	e.semiAutoTick()
}

func (e *Engine) fullAutoTick() {
	if e.clicker.IsActive() {
		return
	}
	if !keystate.IsDown(keystate.VK_LBUTTON) {
		return
	}
	if !keystate.IsDown(keystate.VK_RBUTTON) {
		return
	}

	e.mu.RLock()
	wp := e.wp
	e.mu.RUnlock()

	if wp == nil || !wp.Class.Detail().Type.Has(w.TYPE_FULL_AUTO) {
		return
	}
	speed, speedFrac, spAlt, spAltF := wp.GetAllSpeeds(e.debugging)
	if e.Alt {
		speed = spAlt
		speedFrac = spAltF
	}
	if speed <= 0 {
		return
	}

	e.applyRecoil(speed, int(speedFrac))
}

func (e *Engine) semiAutoTick() {
	defer e.clicker.Stop()
	if !keystate.IsDown(keystate.VK_MBUTTON) {
		return
	}
	if !keystate.IsDown(keystate.VK_RBUTTON) {
		return
	}

	e.mu.RLock()
	wp := e.wp
	e.mu.RUnlock()

	if wp == nil || !wp.Class.Detail().Type.Has(w.TYPE_SEMI_AUTO) {
		return
	}
	speed, speedFrac, spAlt, spAltF := wp.GetAllSpeeds(e.debugging)
	if e.Alt {
		speed = spAlt
		speedFrac = spAltF
	}
	if speed < 0 {
		return
	}

	e.clicker.Feed()
	e.applyRecoil(speed, int(speedFrac))
}

func (e *Engine) applyRecoil(speed, frac int) {
	e.fracCount++
	if e.fracCount > 9 {
		e.fracCount = 0
	}

	dy := speed + e.SpeedOffset
	if dy < 0 {
		dy = 0
	}
	if e.fracCount < frac {
		dy++
	}

	dx := e.jitterMag() * e.horiSign
	e.horiSign = -e.horiSign

	mouse.Move(dx, dy)
}

func (e *Engine) jitterMag() int {
	b := e.HoriJitterBase
	switch {
	case b <= 0:
		return 0
	default:
		r := rand.IntN(4)
		if r == 0 {
			return b - 1
		} else if r == 1 {
			return b + 1
		}
		return b
	}
}

func (e *Engine) DisplayState(
	w interface {
		io.StringWriter
		io.Writer
	},
) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	fmt.Fprintf(w, "| Offset: %d | Jitter: %d |", e.SpeedOffset, e.HoriJitterBase)
}
