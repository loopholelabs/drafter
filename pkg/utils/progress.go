package utils

import (
	"sync"

	"github.com/vbauerster/mpb/v8"
	"github.com/vbauerster/mpb/v8/decor"
)

type ProgressBar struct {
	lock  sync.Mutex
	bar   *mpb.Bar
	total int64
}

func NewProgressBar(
	progress *mpb.Progress,
	label string,
) *ProgressBar {
	return &ProgressBar{
		bar: progress.New(
			0,
			mpb.BarStyle().Lbound("|").Rbound("|"),
			mpb.PrependDecorators(
				decor.Name(label+" ", decor.WCSyncWidthR),
				decor.Percentage(decor.WCSyncWidth),
				decor.Current(decor.SizeB1024(0), " [% .1f", decor.WCSyncWidth),
			),
			mpb.AppendDecorators(
				decor.Total(decor.SizeB1024(0), "% .1f] ", decor.WCSyncWidthR),
				decor.AverageSpeed(decor.SizeB1024(0), "% .1f", decor.WCSyncWidth),
				decor.Name(" ["),
				decor.Elapsed(decor.ET_STYLE_GO, decor.WCSyncWidth),
				decor.Name(":"),
				decor.AverageETA(decor.ET_STYLE_GO, decor.WCSyncWidth),
				decor.Name("]"),
			),
		),
	}
}

func (p *ProgressBar) SetRemaining(remaining int64) {
	p.lock.Lock()
	defer p.lock.Unlock()

	if diff := p.total - remaining; diff > 0 {
		p.bar.SetCurrent(diff)
	}
}

func (p *ProgressBar) SetTotal(total int64) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.total = total
	p.bar.SetTotal(p.total, false)
}

func (p *ProgressBar) IncreaseTotal(delta int64) {
	p.lock.Lock()
	defer p.lock.Unlock()

	p.total += int64(delta)
	p.bar.SetTotal(p.total, false)
}

func (p *ProgressBar) Clear() {
	p.bar.Abort(true)
	p.bar.Wait()
}

func (p *ProgressBar) Pause() func() {
	p.lock.Lock()

	return p.lock.Unlock
}
