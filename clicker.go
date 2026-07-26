package main

import (
	"context"
	"sync"
	"time"

	"github.com/Miuzarte/GoCVStreamer/sendinput"
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
	wake     chan struct{}
}

func newClicker(ctx context.Context) *Clicker {
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

		sendinput.MouseDown(sendinput.MB_LEFT)
		if !c.sleep(clickerDownDuration) {
			return
		}
		sendinput.MouseUp(sendinput.MB_LEFT)
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
