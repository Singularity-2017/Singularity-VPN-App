//go:build windows

package divert

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

// WinDivert's own filter compiler needs neither the driver nor
// administrator, so the filters this package writes are checked against
// it whenever the DLL is beside the build output.
func Test_windivert_compiles_our_filters(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "..", "..", "dist", Library_name))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("%s is not built here", path)
	}
	compile := windows.NewLazyDLL(path).NewProc("WinDivertHelperCompileFilter")
	if err := compile.Find(); err != nil {
		t.Fatal(err)
	}
	filter, err := Outbound_ports_filter([]Port{{Udp, 7086}, {Tcp, 50000}})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{filter, "false", "true"} {
		bytes, _ := windows.BytePtrFromString(text)
		var message *byte
		var position uint32
		ok, _, _ := compile.Call(uintptr(unsafe.Pointer(bytes)), uintptr(Layer_network), 0, 0,
			uintptr(unsafe.Pointer(&message)), uintptr(unsafe.Pointer(&position)))
		if ok == 0 {
			t.Errorf("WinDivert refused %q at %d: %s", text, position, windows.BytePtrToString(message))
		}
	}
}
