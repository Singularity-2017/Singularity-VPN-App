//go:build windows

package openvpn

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

// device_guid is the adapter's fixed identity. Windows keeps a network
// profile per adapter identity; a fresh one at every connect would leave
// "Network 2", "Network 3"... behind.
var device_guid = windows.GUID{Data1: 0x5d1b7a3e, Data2: 0x8c41, Data3: 0x4f0e, Data4: [8]byte{0x9a, 0x27, 0x61, 0xc3, 0x0b, 0x52, 0xe8, 0x14}}

// New_windows_adapter is the adapter over the openvpn.exe at exe, with its
// wintun adapter called device_name and the profile written to
// profile_path, which must be in a directory only LocalSystem and
// administrators can read: the profile holds the client's private key.
func New_windows_adapter(exe, device_name, profile_path string, logger *slog.Logger) *Adapter {
	return New_adapter(windows_starter(exe), func() (device, error) {
		return wintun.CreateAdapter(device_name, "Singularity", &device_guid)
	}, device_name, profile_path, logger)
}

// windows_starter starts openvpn.exe with no window, inside a job object
// that ends it if this service dies, and with a named event to ask it to
// stop: openvpn.exe's --service option, the way its own service stops it,
// which lets it take its routes and DNS settings down on the way out.
func windows_starter(exe string) starter {
	return func(args []string) (process, error) {
		var nonce [8]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return nil, err
		}
		name := "svpn-openvpn-" + hex.EncodeToString(nonce[:])
		name_ptr, err := windows.UTF16PtrFromString(name)
		if err != nil {
			return nil, err
		}
		event, err := windows.CreateEvent(nil, 1, 0, name_ptr)
		if err != nil {
			return nil, fmt.Errorf("creating the stop event: %w", err)
		}
		job, err := kill_on_close_job()
		if err != nil {
			windows.CloseHandle(event)
			return nil, err
		}
		release := func() {
			windows.CloseHandle(job)
			windows.CloseHandle(event)
		}

		reader, writer, err := os.Pipe()
		if err != nil {
			release()
			return nil, err
		}
		cmd := exec.Command(exe, append(args, "--service", name, "0")...)
		cmd.Dir = filepath.Dir(exe)
		cmd.Stdout = writer
		cmd.Stderr = writer
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
		if err := cmd.Start(); err != nil {
			reader.Close()
			writer.Close()
			release()
			return nil, err
		}
		writer.Close()
		if err := assign_to_job(job, cmd.Process.Pid); err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			reader.Close()
			release()
			return nil, err
		}

		p := &windows_process{
			lines: make(chan string, 64),
			done:  make(chan struct{}),
			event: event,
			cmd:   cmd,
		}
		go func() {
			defer close(p.lines)
			defer reader.Close()
			scanner := bufio.NewScanner(reader)
			scanner.Buffer(make([]byte, 4096), 64*1024)
			for scanner.Scan() {
				p.lines <- scanner.Text()
			}
			// A line too long to scan ends the scanning, not the reading:
			// a closed pipe would fail openvpn.exe's next write.
			io.Copy(io.Discard, reader)
		}()
		go func() {
			cmd.Wait()
			p.mu.Lock()
			p.exited = true
			p.mu.Unlock()
			release()
			close(p.done)
		}()
		return p, nil
	}
}

type windows_process struct {
	lines chan string
	done  chan struct{}
	event windows.Handle
	cmd   *exec.Cmd

	mu     sync.Mutex
	exited bool // the handles are closed; nothing may be signalled
}

func (p *windows_process) Lines() <-chan string  { return p.lines }
func (p *windows_process) Done() <-chan struct{} { return p.done }

func (p *windows_process) Stop() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		windows.SetEvent(p.event)
	}
}

func (p *windows_process) Kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited {
		p.cmd.Process.Kill()
	}
}

// kill_on_close_job is a job object that ends every process in it when its
// last handle closes -- when this service exits, however it exits.
func kill_on_close_job() (windows.Handle, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, fmt.Errorf("creating the job object: %w", err)
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		windows.CloseHandle(job)
		return 0, fmt.Errorf("setting up the job object: %w", err)
	}
	return job, nil
}

func assign_to_job(job windows.Handle, pid int) error {
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return fmt.Errorf("opening openvpn.exe: %w", err)
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return fmt.Errorf("putting openvpn.exe in the job object: %w", err)
	}
	return nil
}
