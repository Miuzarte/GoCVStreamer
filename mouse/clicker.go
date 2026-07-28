package mouse

import (
	"context"
	"sync"
	"time"
)

const (
	clickerDownDuration = 15 * time.Millisecond
	clickerUpDuration   = 5 * time.Millisecond
	clickerTimeout      = 5 * time.Second
)

type Clicker struct {
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	active   bool
	lastFeed time.Time
}

func NewClicker(ctx context.Context) *Clicker {
	c := &Clicker{
		lastFeed: time.Now().Add(-clickerTimeout * 2),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

func (c *Clicker) Feed() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastFeed = time.Now()
	if !c.active {
		c.active = true
		go c.loop()
	}
}

func (c *Clicker) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
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

		MouseDown(MB_LEFT)
		if !c.sleep(clickerDownDuration) {
			return
		}
		MouseUp(MB_LEFT)
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
