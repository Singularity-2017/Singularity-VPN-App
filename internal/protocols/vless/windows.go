//go:build windows

package vless

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
	"golang.zx2c4.com/wireguard/windows/tunnel/winipcfg"
	"singularity-vpn-app/internal/xraybundle"
)

// New_windows_adapter runs only the bundled, hash-pinned Xray 26.10.31.
// Construction touches no adapters, routes or processes.
func New_windows_adapter(exe string) *Adapter {
	return new_adapter(func(ctx context.Context, host string) (netip.Addr, error) {
		if a, e := netip.ParseAddr(host); e == nil {
			return a.Unmap(), nil
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil || len(ips) == 0 {
			return netip.Addr{}, errors.New("resolution failed")
		}
		for _, a := range ips {
			if a.Is4() {
				return a, nil
			}
		}
		return ips[0], nil
	}, windows_starter(exe))
}

func lock_bundle(exe string) ([]*os.File, error) {
	if !filepath.IsAbs(exe) || !strings.EqualFold(filepath.Base(exe), "xray.exe") {
		return nil, errors.New("vless: absolute bundled Xray path required")
	}
	var files []*os.File
	fail := func() ([]*os.File, error) {
		for _, f := range files {
			f.Close()
		}
		return nil, errors.New("vless: bundled Xray trust verification failed")
	}
	for _, name := range []string{"xray.exe", "wintun.dll"} {
		want := xraybundle.File_hash(name)
		path := filepath.Join(filepath.Dir(exe), name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fail()
		}
		p, err := windows.UTF16PtrFromString(path)
		if err != nil {
			return fail()
		}
		h, err := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if err != nil {
			return fail()
		}
		f := os.NewFile(uintptr(h), name)
		files = append(files, f)
		hash := sha256.New()
		if _, err := io.Copy(hash, f); err != nil || hex.EncodeToString(hash.Sum(nil)) != want {
			return fail()
		}
	}
	return files, nil
}

type windows_process struct {
	cmd         *exec.Cmd
	done, ready chan struct{}
	ready_once  sync.Once
}

func (p *windows_process) Done() <-chan struct{}  { return p.done }
func (p *windows_process) Ready() <-chan struct{} { return p.ready }
func (p *windows_process) Stop() error {
	select {
	case <-p.done:
		return nil
	default:
	}
	err := p.cmd.Process.Kill()
	<-p.done
	if errors.Is(err, os.ErrProcessDone) {
		return nil
	}
	return err
}

func windows_starter(exe string) starter {
	return func(data []byte) (process, func() error, error) {
		files, err := lock_bundle(exe)
		if err != nil {
			return nil, nil, err
		}
		close_files := func() {
			for _, f := range files {
				f.Close()
			}
		}
		// Never reuse another process's adapter, including the person's v2rayN.
		if _, err := net.InterfaceByName(adapter_name); err == nil {
			close_files()
			return nil, nil, errors.New("vless: tunnel adapter already exists; stop the owning instance first")
		}
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			close_files()
			return nil, nil, errors.New("vless: cannot create process job")
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			close_files()
			return nil, nil, errors.New("vless: cannot secure process job")
		}
		cmd := exec.Command(exe, "run", "-config", "stdin:", "-format", "json")
		cmd.Dir = filepath.Dir(exe)
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		// Do not inherit Xray config/location overrides from the service environment.
		for _, v := range os.Environ() {
			if !strings.HasPrefix(strings.ToUpper(v), "XRAY_") {
				cmd.Env = append(cmd.Env, v)
			}
		}
		input, err := cmd.StdinPipe()
		if err != nil {
			windows.CloseHandle(job)
			close_files()
			return nil, nil, errors.New("vless: cannot open config input")
		}
		output, writer, err := os.Pipe()
		if err != nil {
			input.Close()
			windows.CloseHandle(job)
			close_files()
			return nil, nil, errors.New("vless: cannot open process output")
		}
		cmd.Stdout = writer
		cmd.Stderr = writer
		if err = cmd.Start(); err != nil {
			input.Close()
			output.Close()
			writer.Close()
			windows.CloseHandle(job)
			close_files()
			return nil, nil, errors.New("vless: cannot start bundled Xray")
		}
		writer.Close()
		p := &windows_process{cmd: cmd, done: make(chan struct{}), ready: make(chan struct{})}
		// Xray cannot configure networking until stdin reaches EOF. Assign the job
		// first, so service death even during startup cannot orphan an active TUN.
		handle, e := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
		if e == nil {
			e = windows.AssignProcessToJobObject(job, handle)
			windows.CloseHandle(handle)
		}
		go func() { cmd.Wait(); windows.CloseHandle(job); close_files(); close(p.done) }()
		cleanup := func() error {
			output.Close()
			// On forced exit Xray cannot execute Close. Clear only its own adapter,
			// if Wintun has not already removed it with the child's last handle.
			adapter, e := wintun.OpenAdapter(adapter_name)
			if e != nil {
				return nil
			}
			defer adapter.Close()
			luid := winipcfg.LUID(adapter.LUID())
			var errs []error
			for _, family := range []winipcfg.AddressFamily{windows.AF_INET, windows.AF_INET6} {
				for _, e := range []error{luid.FlushRoutes(family), luid.FlushIPAddresses(family), luid.FlushDNS(family)} {
					if e != nil && !errors.Is(e, windows.ERROR_NOT_FOUND) && !errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
						errs = append(errs, errors.New("vless: tunnel cleanup failed"))
					}
				}
			}
			flush_dns_cache()
			return errors.Join(errs...)
		}
		if e != nil {
			input.Close()
			p.Stop()
			cleanup()
			return nil, nil, errors.New("vless: cannot contain Xray process")
		}
		go func() {
			defer output.Close()
			scan := bufio.NewScanner(output)
			scan.Buffer(make([]byte, 4096), 128*1024)
			for scan.Scan() {
				line := scan.Text()
				if strings.Contains(line, "Xray ") && strings.Contains(line, " started") {
					p.ready_once.Do(func() { flush_dns_cache(); close(p.ready) })
				}
			}
			io.Copy(io.Discard, output)
		}()
		// Config lives in the inherited anonymous pipe, never argv, logs or disk.
		_, err = input.Write(data)
		close_err := input.Close()
		if err != nil || close_err != nil {
			p.Stop()
			cleanup()
			return nil, nil, errors.New("vless: cannot deliver Xray configuration")
		}
		return p, cleanup, nil
	}
}

// Drop cached AAAA answers when entering the IPv4-only proxy, and stale
// tunnel DNS answers when leaving it. The system DLL path is mandatory.
func flush_dns_cache() {
	proc := windows.NewLazySystemDLL("dnsapi.dll").NewProc("DnsFlushResolverCache")
	if proc.Find() == nil {
		proc.Call()
	}
}
