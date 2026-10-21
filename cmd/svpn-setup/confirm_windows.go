//go:build windows

package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"

	"singularity-vpn-app/internal/buildinfo"
)

// confirm asks the person, in a standard dialog, whether to go ahead, and
// says what will be done; everything listed is done, nothing is optional.
// Yes is the default button, so Enter installs.
func confirm(verb string, purge bool) bool {
	var title, text string
	if verb == "install" {
		title = "Установка Singularity VPN"
		text = "Установить Singularity VPN?"
		if installed() {
			text = "Обновить Singularity VPN?"
		}
		if buildinfo.Version != "" {
			text += "\nВерсия " + buildinfo.Version + "."
		}
		text += "\n\nБудет сделано:" +
			"\n•  программа — в Program Files;" +
			"\n•  служба VPN — запускается вместе с Windows;" +
			"\n•  окно — в трее при входе в Windows;" +
			"\n•  ярлык в меню «Пуск» и запись в «Установленных приложениях»." +
			"\n\nWindows спросит права администратора. После установки окно откроется само."
	} else {
		title = "Удаление Singularity VPN"
		text = "Удалить Singularity VPN?\n\nСлужба, файлы, автозапуск и ярлык будут удалены."
		if purge {
			text += " Вход в аккаунт и профиль VPN тоже будут удалены."
		} else {
			text += " Вход в аккаунт и профиль VPN сохранятся."
		}
	}
	const mb_yesno, mb_iconquestion, mb_defbutton1, mb_setforeground = 0x4, 0x20, 0x0, 0x10000
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	answer, err := windows.MessageBox(0, m, t, mb_yesno|mb_iconquestion|mb_defbutton1|mb_setforeground)
	if err != nil {
		return true // no dialog could be shown: the person did start setup
	}
	const idyes = 6
	return answer == idyes
}

// installed reports whether a previous installation is in place.
func installed() bool {
	dir, err := install_dir()
	if err != nil {
		return false
	}
	_, err = os.Stat(filepath.Join(dir, window_exe))
	return err == nil
}
