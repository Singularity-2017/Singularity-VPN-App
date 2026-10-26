//go:build windows

package wireguard

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amnezia-vpn/amneziawg-go/conn"
	"github.com/amnezia-vpn/amneziawg-go/device"
	"github.com/amnezia-vpn/amneziawg-go/tun"
	"golang.org/x/sys/windows"

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
func New_windows_tunnel(name string, mtu int, wrap device_wrapper) (tunnel, error) {
	tun_device, err := create_tun(name, mtu)
	if err != nil {
		return nil, wrap_missing_wintun(err)
	}
	logger := device.NewLogger(device.LogLevelError, fmt.Sprintf("(%s) ", name))
	carried := tun_device
	if wrap != nil {
		carried = wrap(tun_device)
	}
	dev := device.NewDevice(carried, conn.NewDefaultBind(), logger)
	return &real_tunnel{tun_device: tun_device, device: dev}, nil
}

func (t *real_tunnel) Set_config(uapi string) error {
	return t.device.IpcSet(uapi)
}

// Stats reads the peer's counters from the device's UAPI. Only the three
// keys below are looked at; every other line, the private key among them,
// is skipped without being kept.
func (t *real_tunnel) Stats() (Transport_stats, error) {
	text, err := t.device.IpcGet()
	if err != nil {
		return Transport_stats{}, err
	}
	var stats Transport_stats
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "last_handshake_time_sec":
			if seconds, err := strconv.ParseInt(value, 10, 64); err == nil && seconds > 0 {
				stats.Last_handshake = time.Unix(seconds, 0)
			}
		case "tx_bytes":
			stats.Sent, _ = strconv.ParseUint(value, 10, 64)
		case "rx_bytes":
			stats.Received, _ = strconv.ParseUint(value, 10, 64)
		}
	}
	return stats, nil
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

// adapter_gone_wait is how long create_tun keeps trying while the adapter
// of the tunnel just closed is still being removed, and adapter_gone_step
// how often it tries within that.
const (
	adapter_gone_wait = 6 * time.Second
	adapter_gone_step = 250 * time.Millisecond
)

// adapter_guid derives a deterministic GUID from the adapter's name so
// Windows reuses one network profile and one Tcpip\Parameters\Interfaces
// entry across connects instead of creating "Singularity 2", "Singularity 3"...
// and bloating the NSI / Dnscache interface tables.
func adapter_guid(name string) windows.GUID {
	sum := sha256.Sum256([]byte("singularity-vpn/wintun/" + name))
	return windows.GUID{
		Data1: binary.LittleEndian.Uint32(sum[0:4]),
		Data2: binary.LittleEndian.Uint16(sum[4:6]),
		Data3: (binary.LittleEndian.Uint16(sum[6:8]) & 0x0fff) | 0x5000,
		Data4: [8]byte{
			(sum[8] & 0x3f) | 0x80,
			sum[9], sum[10], sum[11], sum[12], sum[13], sum[14], sum[15],
		},
	}
}

// create_tun creates the adapter, waiting out the one just closed. A
// rebuild on the same node closes the old adapter and creates one of the
// same name straight away, and wintun answers "file not found" until the
// old one is fully gone -- seen on the owner's machine 2026-10-21, where
// the first rebuild failed and the second, a moment later, came up.
func create_tun(name string, mtu int) (tun.Device, error) {
	guid := adapter_guid(name)
	deadline := time.Now().Add(adapter_gone_wait)
	for {
		tun_device, err := tun.CreateTUNWithRequestedGUID(name, &guid, mtu)
		// A missing wintun.dll can carry the same code; it does not go
		// away by waiting.
		if err == nil || !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || strings.Contains(err.Error(), "wintun.dll") || time.Now().After(deadline) {
			return tun_device, err
		}
		time.Sleep(adapter_gone_step)
	}
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
