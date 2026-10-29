//go:build windows

package openvpn

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
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
		return keep_device(device_name)
	}, device_name, profile_path, logger)
}

// kept_device is the wintun adapter openvpn.exe runs on, created once and
// kept for the life of the service: creating or removing an adapter is a
// device installation the whole machine pays for (see
// wireguard/kept_tun_windows.go), and the AntiZapret tunnel beside a boost
// used to retry on a new one every minute. The service removes it when it
// stops; one left by a process killed before it could is taken over by the
// next CreateAdapter of the same name and GUID.
var kept_device struct {
	mu      sync.Mutex
	adapter *wintun.Adapter
}

// keep_device hands out the kept adapter, creating it when there is none
// yet or the kept one is gone from the system -- removed in Device Manager,
// or the driver updated under it.
func keep_device(name string) (device, error) {
	kept_device.mu.Lock()
	defer kept_device.mu.Unlock()
	if adapter := kept_device.adapter; adapter != nil {
		// openvpn.exe finds it by name: one renamed in Network Connections
		// is as good as gone.
		if row, err := winipcfg.LUID(adapter.LUID()).Interface(); err == nil && row.Alias() == name {
			return kept_openvpn_device{adapter}, nil
		}
		adapter.Close()
		kept_device.adapter = nil
	}
	adapter, err := wintun.CreateAdapter(name, "Singularity", &device_guid)
	if err != nil {
		return nil, err
	}
	kept_device.adapter = adapter
	return kept_openvpn_device{adapter}, nil
}

// kept_openvpn_device is the kept adapter as one connect's device. Close
// leaves the adapter and clears what openvpn.exe put on it: one killed for
// not stopping in time leaves its routes, addresses and resolvers behind.
type kept_openvpn_device struct{ adapter *wintun.Adapter }

func (d kept_openvpn_device) Close() error {
	luid := winipcfg.LUID(d.adapter.LUID())
	steps := []error{luid.FlushRoutes(windows.AF_UNSPEC), luid.FlushIPAddresses(windows.AF_UNSPEC)}
	// Clearing resolvers is a change every program on the machine reacts
	// to: only when openvpn.exe left some.
	if has_resolvers(device_guid) {
		steps = append(steps, luid.FlushDNS(windows.AF_INET), luid.FlushDNS(windows.AF_INET6))
	}
	var errs []error
	for _, err := range steps {
		if err != nil && !errors.Is(err, windows.ERROR_NOT_FOUND) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// has_resolvers reads whether the adapter has resolvers set from its
// registry keys. LUID.DNS would ask through GetAdaptersAddresses, a call
// into the DNS Client service -- the load this adapter is kept to spare.
func has_resolvers(guid windows.GUID) bool {
	for _, stack := range []string{"Tcpip", "Tcpip6"} {
		key, err := registry.OpenKey(registry.LOCAL_MACHINE, `SYSTEM\CurrentControlSet\Services\`+stack+`\Parameters\Interfaces\`+guid.String(), registry.QUERY_VALUE)
		if err != nil {
			continue
		}
		servers, _, err := key.GetStringValue("NameServer")
		key.Close()
		if err == nil && strings.TrimSpace(servers) != "" {
			return true
		}
	}
	return false
}

// Release_kept_device removes the kept adapter. The service calls it on its
// way out, once the tunnel is down, so a start straight after does not find
// one of the same identity still being removed.
func Release_kept_device() {
	kept_device.mu.Lock()
	defer kept_device.mu.Unlock()
	if kept_device.adapter != nil {
		kept_device.adapter.Close()
		kept_device.adapter = nil
	}
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
