// Package progress is sff's progress-UI primitive: a thin wrapper over
// briandowns/spinner that adds a live elapsed-time counter. It is deliberately
// decoupled from the API clients (pkg/sfapi, pkg/mdapi), which report progress
// through plain callbacks; wire Update into those callbacks to render a spinner.
//
// The spinner animates only when its writer is a TTY, so redirected or piped
// output is never polluted with control characters. On a non-TTY (a server, a
// captured pipe, the IntelliJ external-tool console) it degrades to periodic
// plain-text "<msg> (<elapsed>s)" lines so long operations still show progress,
// and callers can use it unconditionally.
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
	return startOn(os.Stderr, time.Now(), msg)
}

// StartAt is like Start but anchors the elapsed counter to an earlier instant,
// so a sequence of spinners (one per step) shows time accumulating from the
// start of an overall operation rather than resetting on each step.
func StartAt(start time.Time, msg string) *Progress {
	return startOn(os.Stderr, start, msg)
}

// StartOn is like Start but writes to w, letting a library consumer target its
// own terminal. On a TTY it animates a spinner; on a non-TTY (a pipe, CI, or the
// IntelliJ external-tool console) it falls back to periodic plain-text progress
// lines so long operations still show liveness and elapsed time.
func StartOn(w io.Writer, msg string) *Progress {
	return startOn(w, time.Now(), msg)
}

// heartbeat is how often the non-TTY fallback prints an elapsed-time line.
const heartbeat = 5 * time.Second

func startOn(w io.Writer, start time.Time, msg string) *Progress {
	p := &Progress{w: w, start: start, msg: msg}
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	if !isTerminal(w) {
		fmt.Fprintf(w, "%s…\n", msg) // immediate feedback, then heartbeats
		go p.beat()
		return p
	}
	p.sp = spinner.New(spinner.CharSets[11], 100*time.Millisecond,
		spinner.WithWriter(w), spinner.WithHiddenCursor(true))
	p.render()
	p.sp.Start()
	go p.tick() // refresh the elapsed counter even when the message is static
	return p
}

// Update replaces the message shown next to the spinner (or, on a non-TTY,
// recorded for the next heartbeat line).
func (p *Progress) Update(msg string) {
	p.mu.Lock()
	p.msg = msg
	p.mu.Unlock()
	if p.sp != nil {
		p.render()
	}
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

// beat is the non-TTY counterpart of tick: it prints a plain "<msg> (<elapsed>s)"
// line every heartbeat interval so piped/console output shows ongoing progress.
func (p *Progress) beat() {
	defer close(p.doneCh)
	t := time.NewTicker(heartbeat)
	defer t.Stop()
	for {
		select {
		case <-p.stopCh:
			return
		case <-t.C:
			p.mu.Lock()
			msg := p.msg
			p.mu.Unlock()
			fmt.Fprintf(p.w, "%s (%.0fs)\n", msg, time.Since(p.start).Seconds())
		}
	}
}

// Stop halts the animation (clearing the spinner line on a TTY) or the heartbeat
// loop on a non-TTY. It is safe to call once from any goroutine; further calls
// are no-ops.
func (p *Progress) Stop() {
	p.once.Do(func() {
		close(p.stopCh)
		<-p.doneCh
		if p.sp != nil {
			p.sp.Stop()
			fmt.Fprint(p.w, "\r\033[K") // clear the spinner's last line
		}
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
