//go:build windows

package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// enableVirtualTerminalProcessing is the console mode flag that makes the
// Windows console interpret ANSI escape sequences.
const enableVirtualTerminalProcessing = 0x0004

// enableVirtualTerminal turns on ANSI escape processing for the stdout console.
// Windows Terminal already enables it; legacy conhost/cmd.exe needs this call,
// otherwise colors render as literal escape codes. It returns whether the
// console can display ANSI colors (false when stdout isn't a console or the
// mode can't be set, so callers fall back to plain output).
func enableVirtualTerminal() bool {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	getConsoleMode := kernel32.NewProc("GetConsoleMode")
	setConsoleMode := kernel32.NewProc("SetConsoleMode")

	handle := syscall.Handle(os.Stdout.Fd())
	var mode uint32
	if r, _, _ := getConsoleMode.Call(uintptr(handle), uintptr(unsafe.Pointer(&mode))); r == 0 {
		return false // stdout is not a console (e.g. redirected)
	}
	if mode&enableVirtualTerminalProcessing != 0 {
		return true // already enabled (e.g. Windows Terminal)
	}
	r, _, _ := setConsoleMode.Call(uintptr(handle), uintptr(mode|enableVirtualTerminalProcessing))
	return r != 0
}
