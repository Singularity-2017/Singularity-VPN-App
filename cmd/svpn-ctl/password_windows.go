//go:build windows

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/sys/windows"
)

// read_password reads one line from the console with echo off.
//
// The password is never taken from a flag or an argument: a command line
// lands in the shell's history and in the process list, where every other
// account on the machine can read it.
//
// The console mode is restored on every way out, Ctrl+C included. The mode
// belongs to the console, not to this process, so a program that died with
// echo off would leave the person's own shell typing blind after it.
func read_password(prompt string) (string, error) {
	console := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(console, &mode); err != nil {
		return "", errors.New("the password has to be typed at a console; it is never read from a pipe, a flag or an argument")
	}
	if err := windows.SetConsoleMode(console, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", fmt.Errorf("turning off echo: %w", err)
	}
	restore := func() { windows.SetConsoleMode(console, mode) }
	defer restore()

	interrupted := make(chan os.Signal, 1)
	done := make(chan struct{})
	defer close(done)
	signal.Notify(interrupted, os.Interrupt)
	defer signal.Stop(interrupted)
	go func() {
		select {
		case <-interrupted:
			restore()
			fmt.Fprintln(os.Stderr)
			os.Exit(130)
		case <-done:
		}
	}()

	fmt.Fprint(os.Stderr, prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", fmt.Errorf("reading the password: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
