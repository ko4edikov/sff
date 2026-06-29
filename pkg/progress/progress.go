// Package progress is sff's progress-UI primitive: a thin wrapper over
// briandowns/spinner that adds a live elapsed-time counter. It is deliberately
// decoupled from the API clients (pkg/sfapi, pkg/mdapi), which report progress
// through plain callbacks; wire Update into those callbacks to render a spinner.
//
// The spinner animates only when its writer is a TTY, so redirected or piped
// output is never polluted with control characters. On a non-TTY (a server, a
// captured pipe, a test) it is an inert no-op, so callers can use it
// unconditionally.
package progress

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/briandowns/spinner"
)

// Progress is a running spinner with an elapsed-time counter. The zero value is
// not usable; obtain one from Start or StartOn.
type Progress struct {
	w      io.Writer
	sp     *spinner.Spinner
	start  time.Time
	msg    string
	mu     sync.Mutex // guards msg
	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// Start shows an animated spinner labelled msg on stderr and returns a handle.
// Call Update to change the message as work advances (e.g. a deploy status or
// poll count) and Stop exactly once when the operation finishes.
func Start(msg string) *Progress {
	return StartOn(os.Stderr, msg)
}

// StartOn is like Start but writes to w, letting a library consumer target its
// own terminal. When w is not a TTY the returned Progress is inert.
func StartOn(w io.Writer, msg string) *Progress {
	p := &Progress{w: w, start: time.Now(), msg: msg}
	if !isTerminal(w) {
		return p // inert: Update/Stop do nothing
	}
	p.sp = spinner.New(spinner.CharSets[11], 100*time.Millisecond,
		spinner.WithWriter(w), spinner.WithHiddenCursor(true))
	p.render()
	p.sp.Start()
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	go p.tick() // refresh the elapsed counter even when the message is static
	return p
}

// Update replaces the message shown next to the spinner.
func (p *Progress) Update(msg string) {
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
func (p *Progress) render() {
	p.mu.Lock()
	msg := p.msg
	p.mu.Unlock()
	suffix := fmt.Sprintf(" %s (%.1fs)", msg, time.Since(p.start).Seconds())
	p.sp.Lock()
	p.sp.Suffix = suffix
	p.sp.Unlock()
}

func (p *Progress) tick() {
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
func (p *Progress) Stop() {
	p.once.Do(func() {
		if p.sp == nil {
			return
		}
		close(p.stopCh)
		<-p.doneCh
		p.sp.Stop()
		fmt.Fprint(p.w, "\r\033[K") // clear the spinner's last line
	})
}

// isTerminal reports whether w is a character device (a TTY).
func isTerminal(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
