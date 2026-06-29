package mdapi

import (
	"fmt"
	"io"
)

// Logger receives verbose Metadata API tracing. A nil Logger on a Client
// disables tracing, so the package is silent by default — the caller decides
// whether and where traces are written. See NewWriterLogger for the common case.
type Logger interface {
	Debugf(format string, args ...any)
}

// NewWriterLogger returns a Logger that writes each trace line to w, prefixed
// with prefix and terminated by a newline. Pass the result to Client.Logger to
// enable tracing (e.g. NewWriterLogger(os.Stderr, "[sff:mdapi] ")).
func NewWriterLogger(w io.Writer, prefix string) Logger {
	return &writerLogger{w: w, prefix: prefix}
}

type writerLogger struct {
	w      io.Writer
	prefix string
}

func (l *writerLogger) Debugf(format string, args ...any) {
	fmt.Fprintf(l.w, l.prefix+format+"\n", args...)
}

// debugf forwards to the Client's Logger when one is set; otherwise it is a no-op.
func (c *Client) debugf(format string, args ...any) {
	if c.Logger != nil {
		c.Logger.Debugf(format, args...)
	}
}
