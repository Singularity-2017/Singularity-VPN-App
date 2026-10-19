//go:build windows

// Command svpn-setup installs and removes Singularity VPN: the files under
// Program Files, the service, the window's autostart and Start menu entry,
// and the Installed apps entry. It carries the binaries inside itself (see
// payload_embedded_windows.go) and asks for administrator once, through the
// standard prompt; the window it starts afterwards runs as the person, not
// as administrator.
package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/buildinfo"
)

const elevated_flag = "--elevated"

func main() {
	enable_colors()

	args := os.Args[1:]
	elevated_child := take(&args, elevated_flag)
	purge := take(&args, "--purge")
	verb := "install"
	if len(args) > 0 {
		verb = args[0]
	}
	switch verb {
	case "install", "uninstall":
	default:
		usage()
		os.Exit(2)
	}

	if !windows.GetCurrentProcessToken().IsElevated() {
		forwarded := []string{verb, elevated_flag}
		if purge {
			forwarded = append(forwarded, "--purge")
		}
		code, err := run_elevated(forwarded)
		if err != nil {
			if errors.Is(err, err_declined) {
				bad("Без прав администратора установить службу нельзя; ничего не изменено.")
			} else {
				bad("Не удалось запросить права администратора: %v", err)
			}
			os.Exit(1)
		}
		if code != 0 {
			bad("Не получилось; подробности были в окне администратора.")
			os.Exit(int(code))
		}
		finish(verb)
		return
	}

	var err error
	if verb == "install" {
		err = install()
	} else {
		err = uninstall(purge)
	}
	if err != nil {
		bad("\n%v", err)
		if elevated_child {
			wait_for_enter()
		}
		os.Exit(1)
	}
	if !elevated_child {
		finish(verb)
	}
}

// finish runs where the person started setup: after an install it opens
// the window, unelevated.
func finish(verb string) {
	if verb != "install" {
		ok("Singularity VPN удалён.")
		return
	}
	ok("Singularity VPN установлен.")
	if dir, err := install_dir(); err == nil {
		if err := launch_unelevated(filepath.Join(dir, window_exe)); err != nil {
			warn("Окно не открылось само: %v. Оно есть в меню «Пуск».", err)
		}
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "Singularity VPN setup.")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "  "+command("svpn-setup")+"                      install or update")
	fmt.Fprintln(os.Stderr, "  "+command("svpn-setup uninstall")+"            remove; the sign-in and VPN profile are kept")
	fmt.Fprintln(os.Stderr, "  "+command("svpn-setup uninstall --purge")+"    remove, and delete the sign-in and VPN profile too")
}

func take(args *[]string, flag string) bool {
	for i, a := range *args {
		if a == flag {
			*args = append((*args)[:i], (*args)[i+1:]...)
			return true
		}
	}
	return false
}

func install() error {
	files, err := payload_contents(payload_source())
	if err != nil {
		return err
	}
	dir, err := install_dir()
	if err != nil {
		return fmt.Errorf("cannot locate Program Files: %w", err)
	}
	fmt.Println("Установка Singularity VPN в " + dir)

	// Only administrators may write here, set explicitly rather than
	// inherited (see install_security): a LocalSystem service runs from
	// this directory.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := secure_install_dir(dir); err != nil {
		return fmt.Errorf("protecting %s: %w", dir, err)
	}

	if n, err := close_windows_from(dir); err != nil {
		warn("  не удалось проверить запущенные окна: %v", err)
	} else if n > 0 {
		step("закрыто окон приложения: %d", n)
	}

	existed, err := stop_service()
	if err != nil {
		return fmt.Errorf("stopping the running service: %w", err)
	}
	if existed {
		step("служба остановлена")
		// Whatever fails from here on, a person's VPN must not stay off
		// because an update stopped halfway: the service goes back up,
		// on the old files or the new.
		defer func() {
			if !service_running() {
				if err := start_service(); err != nil {
					bad("  службу не удалось запустить снова: %v", err)
				}
			}
		}()
	}

	var total int64
	for _, name := range payload_files {
		if err := write_replacing(filepath.Join(dir, name), files[name]); err != nil {
			return fmt.Errorf("writing %s: %w", name, err)
		}
		total += int64(len(files[name]))
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if target := filepath.Join(dir, uninstaller); !strings.EqualFold(self, target) {
		data, err := os.ReadFile(self)
		if err != nil {
			return fmt.Errorf("reading this setup to keep it as the uninstaller: %w", err)
		}
		if err := write_replacing(target, data); err != nil {
			return fmt.Errorf("writing %s: %w", uninstaller, err)
		}
		total += int64(len(data))
	}
	step("файлы записаны")

	service_binary := filepath.Join(dir, service_exe)
	if existed {
		if err := point_service_at(service_binary); err != nil {
			return fmt.Errorf("pointing the service at %s: %w", service_binary, err)
		}
	} else {
		// svpn-service registers itself: start type, LocalSystem, the
		// restart-on-failure actions and the event log source all live
		// in one place, its install command.
		cmd := exec.Command(service_binary, "install")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("registering the service: %w", err)
		}
	}
	if err := start_service(); err != nil {
		return fmt.Errorf("starting the service: %w", err)
	}
	step("служба запущена")

	if err := set_autostart(dir); err != nil {
		return fmt.Errorf("adding the window to autostart: %w", err)
	}
	if path, err := shortcut_path(); err == nil {
		if err := create_shortcut(path, filepath.Join(dir, window_exe), dir); err != nil {
			warn("  ярлык в меню «Пуск» не создан: %v", err)
		}
	}
	if err := write_uninstall_entry(uninstall_entry(dir, buildinfo.Version, uint32(total/1024))); err != nil {
		return fmt.Errorf("adding the Installed apps entry: %w", err)
	}
	step("автозапуск, меню «Пуск» и «Установленные приложения» настроены")
	return nil
}

func uninstall(purge bool) error {
	dir, err := install_dir()
	if err != nil {
		return fmt.Errorf("cannot locate Program Files: %w", err)
	}
	fmt.Println("Удаление Singularity VPN из " + dir)

	if n, err := close_windows_from(dir); err == nil && n > 0 {
		step("закрыто окон приложения: %d", n)
	}

	service_binary := filepath.Join(dir, service_exe)
	if _, err := os.Stat(service_binary); err == nil {
		// svpn-service's uninstall stops the service -- tunnel down first
		// -- waits for it, and removes it with its event log source.
		cmd := exec.Command(service_binary, "uninstall")
		cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			if existed, _ := stop_service(); existed {
				return fmt.Errorf("removing the service: %w; nothing else was removed", err)
			}
		}
	}

	if err := remove_autostart(); err != nil {
		warn("  автозапуск не убран: %v", err)
	}
	if path, err := shortcut_path(); err == nil {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			warn("  ярлык не удалён: %v", err)
		}
	}
	if err := remove_uninstall_entry(); err != nil {
		warn("  запись в «Установленных приложениях» не удалена: %v", err)
	}

	self, _ := os.Executable()
	for _, name := range append(append([]string{}, payload_files...), uninstaller) {
		path := filepath.Join(dir, name)
		if strings.EqualFold(path, self) {
			if err := delete_at_reboot(path); err != nil {
				warn("  %s останется: %v", name, err)
			}
			continue
		}
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			warn("  %s не удалён: %v", name, err)
		}
	}
	if err := os.Remove(dir); err != nil && !errors.Is(err, os.ErrNotExist) {
		// The running uninstaller is still in it; Windows removes the
		// directory with it at the next start.
		delete_at_reboot(dir)
	}
	step("файлы удалены")

	data, err := data_dir()
	if err != nil {
		return nil
	}
	if purge {
		if err := os.RemoveAll(data); err != nil {
			return fmt.Errorf("deleting %s: %w", data, err)
		}
		step("вход и VPN-профиль этого компьютера удалены (%s)", data)
	} else if _, err := os.Stat(data); err == nil {
		step("вход и VPN-профиль оставлены в %s — повторная установка их подхватит", data)
	}
	return nil
}
