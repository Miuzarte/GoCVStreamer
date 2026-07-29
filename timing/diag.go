package timing

import (
	"time"

	"github.com/rs/zerolog"
)

type Diag struct {
	name   string
	alpha  float64
	avgMs  float64
	thresh float64
	minMs  float64
	warmup int
	count  int

	lastTrace     time.Time
	traceInterval time.Duration
}

func NewDiag(name string) *Diag {
	return &Diag{
		name:          name,
		alpha:         0.9,
		thresh:        3.0,
		minMs:         10.0,
		warmup:        10,
		traceInterval: time.Second,
	}
}

func (d *Diag) Observe(dur time.Duration, log zerolog.Logger) {
	ms := float64(dur) / float64(time.Millisecond)
	d.count++

	if d.avgMs == 0 {
		d.avgMs = ms
	} else {
		d.avgMs = d.avgMs*d.alpha + ms*(1-d.alpha)
	}

	if d.count <= d.warmup {
		return
	}

	if ms < d.minMs {
		return
	}

	lvl := zerolog.TraceLevel
	msg := ""
	if ms > d.avgMs*d.thresh {
		lvl = zerolog.WarnLevel
		msg = " SLOW"
	}

	if lvl == zerolog.WarnLevel {
		log.WithLevel(lvl).
			Float64("ms", ms).
			Float64("avg_ms", d.avgMs).
			Int("count", d.count).
			Msgf("%s: %.1fms (avg: %.1fms)%s", d.name, ms, d.avgMs, msg)
		return
	}

	if time.Since(d.lastTrace) >= d.traceInterval {
		d.lastTrace = time.Now()
		log.WithLevel(lvl).
			Float64("ms", ms).
			Float64("avg_ms", d.avgMs).
			Int("count", d.count).
			Msgf("%s: %.1fms (avg: %.1fms)", d.name, ms, d.avgMs)
	}
}
