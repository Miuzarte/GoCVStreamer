package main

import (
	"math/rand/v2"
	"sync"

	"github.com/Miuzarte/GoCVStreamer/keystate"
	"github.com/Miuzarte/GoCVStreamer/sendinput"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
)

type Engine struct {
	mu     sync.RWMutex
	weapon *w.Weapon

	keys    *keystate.Tracker
	clicker *Clicker
	Alt     bool

	SpeedOffset    int
	HoriJitterBase int
	horiSign       int

	fracCount int
}

func newEngine(clicker *Clicker) *Engine {
	return &Engine{
		keys:           keystate.NewTracker(),
		clicker:        clicker,
		SpeedOffset:    -4,
		HoriJitterBase: 2,
		horiSign:       1,
	}
}

func (e *Engine) Close() {
	e.clicker.Close()
}

func (e *Engine) SetWeapon(weap *w.Weapon) {
	e.mu.Lock()
	e.weapon = weap
	e.mu.Unlock()
}

func (e *Engine) Tick() {
	shift := keystate.IsDown(keystate.VK_SHIFT)

	if e.keys.Pressed(keystate.VK_OEM_PLUS) {
		if !shift {
			e.SpeedOffset++
			log.Info().
				Int("Offset", e.SpeedOffset).
				Send()
		} else {
			e.HoriJitterBase++
			log.Info().
				Int("Jitter", e.HoriJitterBase).
				Send()
		}
	}
	if e.keys.Pressed(keystate.VK_OEM_MINUS) {
		if !shift {
			e.SpeedOffset--
			log.Info().
				Int("Offset", e.SpeedOffset).
				Send()
		} else {
			e.HoriJitterBase--
			log.Info().
				Int("Jitter", e.HoriJitterBase).
				Send()
		}
	}

	if e.keys.Pressed(keystate.VK_INSERT) {
		e.Alt = !e.Alt
		log.Info().
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
	weap := e.weapon
	e.mu.RUnlock()

	if weap == nil || !weap.Class.Detail().Type.Has(w.TYPE_FULL_AUTO) {
		return
	}
	speed, fracU, spAlt, spAltFU := weap.GetAllSpeeds(debugging)
	frac := int(fracU)
	if e.Alt {
		speed = spAlt
		frac = int(spAltFU)
	}
	if speed <= 0 {
		return
	}
	e.applyRecoil(speed, frac)
}

func (e *Engine) semiAutoTick() {
	if !keystate.IsDown(keystate.VK_MBUTTON) {
		e.clicker.Stop()
		return
	}
	if !keystate.IsDown(keystate.VK_RBUTTON) {
		e.clicker.Stop()
		return
	}

	e.mu.RLock()
	weap := e.weapon
	e.mu.RUnlock()

	if weap == nil || !weap.Class.Detail().Type.Has(w.TYPE_SEMI_AUTO) {
		return
	}
	speed, fracU, spAlt, spAltFU := weap.GetAllSpeeds(debugging)
	frac := int(fracU)
	if e.Alt {
		speed = spAlt
		frac = int(spAltFU)
	}
	if speed < 0 {
		e.clicker.Stop()
		return
	}

	e.clicker.Feed()
	e.applyRecoil(speed, frac)
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

	sendinput.Move(dx, dy)
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
