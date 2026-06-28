//go:build !windows

package cli

// enableVirtualTerminal is a no-op on non-Windows platforms, whose terminals
// already interpret ANSI escapes; it reports that colors are supported.
func enableVirtualTerminal() bool { return true }
