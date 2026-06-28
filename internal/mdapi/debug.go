package mdapi

import (
	"fmt"
	"os"
)

// debugOn enables verbose Metadata API tracing to stderr when SFF_DEBUG is set.
var debugOn = os.Getenv("SFF_DEBUG") != ""

func debugf(format string, args ...any) {
	if debugOn {
		fmt.Fprintf(os.Stderr, "[sff:mdapi] "+format+"\n", args...)
	}
}
