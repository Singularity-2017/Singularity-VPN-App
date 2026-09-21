//go:build windows

package wireguard

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun"

	"singularity-vpn-app/internal/platform"
)

// real_tunnel wraps a wireguard-go device behind the tunnel interface. This
// is the one part of this package that cannot be exercised without
// administrator and a real network adapter, and it is written to have no
// decision of its own left to make: everything that can be got wrong is in
// adapter.go instead, where a test can see it.
type real_tunnel struct {
	tun_device tun.Device
	device     *device.Device
}

// New_windows_tunnel is the tunnel_factory New_windows_adapter wires in.
//
// The logger is set to device.LogLevelError, not LogLevelVerbose:
// wireguard-go's verbose level logs handshake detail, which this process
// must never print regardless of how tempting that is to turn on while
// debugging a tunnel that will not come up.
func New_windows_tunnel(name string, mtu int) (tunnel, error) {
	tun_device, err := tun.CreateTUN(name, mtu)
	if err != nil {
		return nil, wrap_missing_wintun(err)
	}
	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("(%s) ", name))
	dev := device.NewDevice(tun_device, conn.NewDefaultBind(), logger)
	return &real_tunnel{tun_device: tun_device, device: dev}, nil
}

func (t *real_tunnel) Set_config(uapi string) error {
	return t.device.IpcSet(uapi)
}

func (t *real_tunnel) Up() error {
	return t.device.Up()
}

// Close never actually fails: (*device.Device).Close returns nothing. The
// tunnel interface still declares an error, because adapter.go's unwind
// test needs a fake that can.
func (t *real_tunnel) Close() error {
	t.device.Close()
	return nil
}

func (t *real_tunnel) Handle() platform.Tunnel_handle {
	native, ok := t.tun_device.(*tun.NativeTun)
	if !ok {
		return 0
	}
	return platform.Tunnel_handle(native.LUID())
}

// wrap_missing_wintun turns tun.CreateTUN's "Error loading wintun.dll DLL:
// ..." -- a bare LoadLibraryEx failure two layers down -- into a message
// that names the file and where a person can put it. wintun.dll is not a Go
// dependency: wireguard-go loads it at runtime from the directory the
// executable sits in, or System32, and it is not part of this repository.
func wrap_missing_wintun(err error) error {
	if !strings.Contains(err.Error(), "wintun.dll") {
		return fmt.Errorf("wireguard: creating tunnel device: %w", err)
	}
	exe_dir := "the directory the executable is in"
	if path, dir_err := os.Executable(); dir_err == nil {
		exe_dir = filepath.Dir(path)
	}
	return fmt.Errorf(
		"wireguard: wintun.dll was not found in %s or in System32 -- it ships separately from this program "+
			"and must be placed next to the executable or installed system-wide: %w",
		exe_dir, err,
	)
}
