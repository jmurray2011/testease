//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// Windows has no signals. testease reads Ctrl+Break as an interrupt, so a
// child is started in a process group of its own, and stopping it sends Ctrl+
// Break to that group alone. This app is a windowed program with no console;
// a console program it starts would open a console window of its own, so the
// child gets a hidden one, and sending the event means borrowing that console
// for a moment.
const (
	createNewProcessGroup = 0x00000200
	createNoWindow        = 0x08000000
	ctrlBreakEvent        = 1
)

var (
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procAttachConsole        = kernel32.NewProc("AttachConsole")
	procFreeConsole          = kernel32.NewProc("FreeConsole")
	procGetConsoleCP         = kernel32.NewProc("GetConsoleCP")
	procGenerateConsoleCtrl  = kernel32.NewProc("GenerateConsoleCtrlEvent")
	procSetConsoleCtrlHandle = kernel32.NewProc("SetConsoleCtrlHandler")
)

func hasConsole() bool {
	cp, _, _ := procGetConsoleCP.Call()
	return cp != 0
}

// prepare starts the child in its own process group, so an interrupt reaches
// it and nothing else. With a console of our own the child shares it;
// without one it gets a hidden one rather than a window.
func prepare(cmd *exec.Cmd) {
	flags := uint32(createNewProcessGroup)
	if !hasConsole() {
		flags |= createNoWindow
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags}
}

func interrupt(cmd *exec.Cmd) error {
	pid := uintptr(cmd.Process.Pid)
	if hasConsole() {
		return ctrlBreak(pid)
	}
	if ok, _, err := procAttachConsole.Call(pid); ok == 0 {
		return fmt.Errorf("reach the run's console: %w", err)
	}
	defer procFreeConsole.Call() //nolint:errcheck // nothing to do if it fails
	// Attached, this app would get the event too; ignore it for the moment.
	procSetConsoleCtrlHandle.Call(0, 1)       //nolint:errcheck // best effort
	defer procSetConsoleCtrlHandle.Call(0, 0) //nolint:errcheck // best effort
	return ctrlBreak(pid)
}

func ctrlBreak(group uintptr) error {
	if ok, _, err := procGenerateConsoleCtrl.Call(ctrlBreakEvent, group); ok == 0 {
		return fmt.Errorf("interrupt the run: %w", err)
	}
	return nil
}
