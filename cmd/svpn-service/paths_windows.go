//go:build windows

package main

import (
	"errors"
	"fmt"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// paths is where the service keeps everything it owns on disk.
type paths struct {
	root    string // %ProgramData%\Singularity VPN
	state   string // state.json
	secrets string // secrets.json
	logs    string // logs
	log     string // logs\service.log
}

// service_paths asks Windows where ProgramData is rather than reading
// %ProgramData%: an environment variable is something whoever starts this
// process can set, and a service that stores a credential wherever its
// environment points is one a caller can redirect.
func service_paths() (paths, error) {
	program_data, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		return paths{}, fmt.Errorf("locating ProgramData: %w", err)
	}
	root := filepath.Join(program_data, "Singularity VPN")
	logs := filepath.Join(root, "logs")
	return paths{
		root:    root,
		state:   filepath.Join(root, "state.json"),
		secrets: filepath.Join(root, "secrets.json"),
		logs:    logs,
		log:     filepath.Join(logs, "service.log"),
	}, nil
}

// data_security is what actually protects the device token: see the note
// on machine scope in internal/security/dpapi_windows.go.
//
//	O:BA              owned by Administrators rather than by whoever ran
//	                  the service, for the reason spelled out on the pipe's
//	                  descriptor in internal/ipc/pipe_windows.go
//	D:P               protected: nothing inherited from ProgramData, whose
//	                  default ACL lets Users create and write -- inheriting
//	                  it would leave a credential readable machine-wide
//	(A;OICI;GA;;;SY)  LocalSystem, the service, inherited by everything
//	(A;OICI;GA;;;BA)  Administrators, so the machine's owner can diagnose it
//	(A;OICI;RC;;;OW)  OWNER RIGHTS: the owner may read the ACL and nothing
//	                  more. Files the service creates are owned by the
//	                  creating token, which for `svpn-service run` from an
//	                  elevated console is the ordinary user -- and an owner
//	                  may by default rewrite a file's ACL whatever it says.
//	                  Without this entry, any program running as that user,
//	                  unelevated, could grant itself read on secrets.json
//	                  and open it by full path, since Windows lets everyone
//	                  bypass traverse checks on the directories above it.
//
// Nobody else at all.
const data_security = `O:BAD:P(A;OICI;GA;;;SY)(A;OICI;GA;;;BA)(A;OICI;RC;;;OW)`

// ensure_directories creates the root and the log directory with
// data_security, or repairs them to it.
func ensure_directories(p paths) error {
	for _, dir := range []string{p.root, p.logs} {
		if err := ensure_private_directory(dir); err != nil {
			return err
		}
	}
	return nil
}

// ensure_private_directory creates dir with data_security. A directory that
// already exists has its security set explicitly instead: CreateDirectory
// ignores the security attributes when it fails with ERROR_ALREADY_EXISTS,
// so relying on creation alone would leave an installation from a build
// that got this wrong -- or a directory somebody made by hand -- exactly as
// open as it was. Setting it again when it is already right costs nothing.
func ensure_private_directory(dir string) error {
	descriptor, err := windows.SecurityDescriptorFromString(data_security)
	if err != nil {
		return fmt.Errorf("parsing the data directory's security descriptor: %w", err)
	}

	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, err)
	}
	attributes := windows.SecurityAttributes{SecurityDescriptor: descriptor}
	attributes.Length = uint32(unsafe.Sizeof(attributes))

	err = windows.CreateDirectory(name, &attributes)
	if err == nil {
		return nil
	}
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	owner, _, err := descriptor.Owner()
	if err != nil {
		return fmt.Errorf("reading the owner from the data directory's descriptor: %w", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("reading the DACL from the data directory's descriptor: %w", err)
	}
	// SetNamedSecurityInfo also pushes the inheritable entries down to
	// everything already inside, which is what repairs an old secrets.json.
	err = windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		owner, nil, dacl, nil)
	if err != nil {
		return fmt.Errorf("securing the existing %s: %w", dir, err)
	}
	return nil
}
