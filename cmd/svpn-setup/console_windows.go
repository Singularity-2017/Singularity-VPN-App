//go:build windows

package main

import (
	"bufio"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

var colors = false

// enable_colors turns on ANSI colours when stdout is a console, including
// the classic conhost, and NO_COLOR is not set. Redirected to a file or a
// pipe, the output stays plain text.
func enable_colors() {
	if os.Getenv("NO_COLOR") != "" {
		return
	}
	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if windows.GetConsoleMode(handle, &mode) != nil {
		return
	}
	if windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING) != nil {
		return
	}
	colors = true
}

func paint(code, text string) string {
	if !colors {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

func step(format string, args ...any) { fmt.Printf("  "+format+"\n", args...) }
func ok(format string, args ...any)   { fmt.Println(paint("32", fmt.Sprintf(format, args...))) }
func warn(format string, args ...any) { fmt.Println(paint("33", fmt.Sprintf(format, args...))) }
func bad(format string, args ...any) {
	fmt.Fprintln(os.Stderr, paint("31", fmt.Sprintf(format, args...)))
}

// command renders a command printed for a person to run, in grey.
func command(text string) string { return paint("90", text) }

// wait_for_enter keeps a console that only this process owns -- the one
// the elevation prompt opened -- on screen until the person has read it.
func wait_for_enter() {
	fmt.Print("\nНажмите Enter, чтобы закрыть окно.")
	bufio.NewReader(os.Stdin).ReadString('\n')
}
