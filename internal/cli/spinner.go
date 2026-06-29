package cli

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/briandowns/spinner"
)

// progress is sff's single progress-UI primitive: a thin wrapper over
// briandowns/spinner that adds a live elapsed-time counter. It animates on
// stderr only when stderr is a TTY, so redirected/piped output and the data
// written to stdout are never polluted with control characters. On a non-TTY it
// is an inert no-op, so callers can use it unconditionally.
type progress struct {
	sp     *spinner.Spinner
	start  time.Time
	msg    string
	mu     sync.Mutex // guards msg
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// startProgress shows an animated spinner labelled msg on stderr and returns a
// handle. Call Update to change the message as work advances (e.g. a deploy
// status or poll count) and Stop exactly once when the operation finishes.
func startProgress(msg string) *progress {
	p := &progress{start: time.Now(), msg: msg}
	if !isTerminal(os.Stderr) {
		return p // inert: Update/Stop do nothing
	}
	p.sp = spinner.New(spinner.CharSets[11], 100*time.Millisecond,
		spinner.WithWriter(os.Stderr), spinner.WithHiddenCursor(true))
	p.render()
	p.sp.Start()
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	go p.tick() // refresh the elapsed counter even when the message is static
	return p
}

// Update replaces the message shown next to the spinner.
func (p *progress) Update(msg string) {
	if p.sp == nil {
		return
	}
	p.mu.Lock()
	p.msg = msg
	p.mu.Unlock()
	p.render()
}

// render writes "<msg> (<elapsed>s)" into the spinner suffix under the
// spinner's own lock, so it never races the animation goroutine.
func (p *progress) render() {
	p.mu.Lock()
	msg := p.msg
	p.mu.Unlock()
	suffix := fmt.Sprintf(" %s (%.1fs)", msg, time.Since(p.start).Seconds())
	p.sp.Lock()
	p.sp.Suffix = suffix
	p.sp.Unlock()
}

func (p *progress) tick() {
	defer close(p.doneCh)
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.render()
		}
	}
}

// Stop halts the animation and clears the spinner line. It is safe to call once
// from any goroutine; further calls are no-ops.
func (p *progress) Stop() {
	p.once.Do(func() {
		if p.sp == nil {
			return
		}
		close(p.stopCh)
		<-p.doneCh
		p.sp.Stop()
		fmt.Fprint(os.Stderr, "\r\033[K") // clear the spinner's last line
	})
}
