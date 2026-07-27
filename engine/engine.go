package engine

import (
	"context"
	"fmt"
	"image"
	"math/rand/v2"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/Miuzarte/GoCVStreamer/keystate"
	"github.com/Miuzarte/GoCVStreamer/logger"
	"github.com/Miuzarte/GoCVStreamer/mouse"
	"github.com/Miuzarte/GoCVStreamer/ui"
	w "github.com/Miuzarte/GoCVStreamer/weapon"
	"github.com/rs/zerolog"
)

const (
	clickerDownDuration = 15 * time.Millisecond
	clickerUpDuration   = 5 * time.Millisecond
	clickerTimeout      = 5 * time.Second
)

type Config struct {
	Debugging bool
}

type Engine struct {
	log zerolog.Logger
	mu  sync.RWMutex
	wp  *w.Weapon

	keys    *keystate.Tracker
	clicker *Clicker
	Alt     bool

	SpeedOffset    int
	HoriJitterBase int
	horiSign       int
	fracCount      int

	debugging bool
}

func New(clicker *Clicker, cfg Config) *Engine {
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
			e.log.Info().Int("Offset", e.SpeedOffset).Send()
		} else {
			e.HoriJitterBase++
			e.log.Info().Int("Jitter", e.HoriJitterBase).Send()
		}
	}
	if e.keys.Pressed(keystate.VK_OEM_MINUS) {
		if !shift {
			e.SpeedOffset--
			e.log.Info().Int("Offset", e.SpeedOffset).Send()
		} else {
			e.HoriJitterBase--
			e.log.Info().Int("Jitter", e.HoriJitterBase).Send()
		}
	}

	if e.keys.Pressed(keystate.VK_INSERT) {
		e.Alt = !e.Alt
		e.log.Info().Bool("Alt", e.Alt).Send()
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
	weap := e.wp
	e.mu.RUnlock()

	if weap == nil || !weap.Class.Detail().Type.Has(w.TYPE_FULL_AUTO) {
		return
	}
	speed, fracU, spAlt, spAltFU := weap.GetAllSpeeds(e.debugging)
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
	weap := e.wp
	e.mu.RUnlock()

	if weap == nil || !weap.Class.Detail().Type.Has(w.TYPE_SEMI_AUTO) {
		return
	}
	speed, fracU, spAlt, spAltFU := weap.GetAllSpeeds(e.debugging)
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

func (e *Engine) Draw(gtx layout.Context, s ui.DScale) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	defer op.Offset(image.Pt(0, ui.FontSize*5)).Push(gtx.Ops).Pop()

	ui.DrawList(gtx, []string{
		fmt.Sprintf("Offset: %d", e.SpeedOffset),
		fmt.Sprintf("Jitter: %d", e.HoriJitterBase),
	})
}

type Clicker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   bool
	lastFeed time.Time
	wake     chan struct{}
}

func NewClicker(ctx context.Context) *Clicker {
	c := &Clicker{
		lastFeed: time.Now().Add(-clickerTimeout * 2),
		wake:     make(chan struct{}, 1),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

func (c *Clicker) Feed() {
	c.mu.Lock()
	c.lastFeed = time.Now()
	wasActive := c.active
	if !wasActive {
		c.active = true
		c.mu.Unlock()
		go c.loop()
	} else {
		c.mu.Unlock()
	}
}

func (c *Clicker) Stop() {
	c.mu.Lock()
	c.active = false
	c.mu.Unlock()
}

func (c *Clicker) IsActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

func (c *Clicker) Close() {
	c.cancel()
}

func (c *Clicker) loop() {
	for {
		c.mu.Lock()
		if !c.active {
			c.mu.Unlock()
			return
		}
		if time.Since(c.lastFeed) > clickerTimeout {
			c.active = false
			c.mu.Unlock()
			return
		}
		c.mu.Unlock()

		mouse.MouseDown(mouse.MB_LEFT)
		if !c.sleep(clickerDownDuration) {
			return
		}
		mouse.MouseUp(mouse.MB_LEFT)
		if !c.sleep(clickerUpDuration) {
			return
		}
	}
}

func (c *Clicker) sleep(d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
