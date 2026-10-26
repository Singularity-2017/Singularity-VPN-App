package main

import (
	"debug/buildinfo"
	"errors"
	"fmt"
	"strings"

	"singularity-vpn-app/internal/update"
)

// linker_words follows Go's quoted-field rules: quotes only group a field
// when they start it, and there is no unescaping inside a quoted field.
func linker_words(flags string) ([]string, error) {
	var words []string
	for {
		flags = strings.TrimLeft(flags, " \t\r\n")
		if flags == "" {
			return words, nil
		}
		if flags[0] == '\'' || flags[0] == '"' {
			end := strings.IndexByte(flags[1:], flags[0])
			if end < 0 {
				return nil, errors.New("invalid quoted build metadata")
			}
			words = append(words, flags[1:end+1])
			flags = flags[end+2:]
			continue
		}
		end := strings.IndexAny(flags, " \t\r\n")
		if end < 0 {
			words = append(words, flags)
			return words, nil
		}
		words = append(words, flags[:end])
		flags = flags[end:]
	}
}

func artifact_version_flags(flags string) (string, error) {
	words, err := linker_words(flags)
	if err != nil {
		return "", err
	}
	const variable = "singularity-vpn-app/internal/buildinfo.Version="
	var version string
	for i := 0; i < len(words); i++ {
		value := ""
		if words[i] == "-X" && i+1 < len(words) {
			i++
			value = words[i]
		} else if strings.HasPrefix(words[i], "-X=") {
			value = strings.TrimPrefix(words[i], "-X=")
		}
		if strings.HasPrefix(value, variable) {
			version = strings.TrimPrefix(value, variable)
		}
	}
	if version == "" {
		return "", errors.New("setup has no embedded application version")
	}
	return version, nil
}

func setup_version(path string) (string, error) {
	info, err := buildinfo.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("cannot read setup build metadata: %w", err)
	}
	if info.Path != "singularity-vpn-app/cmd/svpn-setup" {
		return "", errors.New("artifact is not the Singularity VPN setup")
	}
	for _, setting := range info.Settings {
		if setting.Key == "-ldflags" {
			return artifact_version_flags(setting.Value)
		}
	}
	return "", errors.New("setup has no embedded application version")
}

func match_artifact_version(actual, expected string) error {
	if _, ok := update.Parse_version(actual); !ok {
		return errors.New("setup is not a release build (missing, dirty or development version)")
	}
	if actual != expected {
		return fmt.Errorf("setup version %s differs from manifest version %s", actual, expected)
	}
	return nil
}

func check_setup_version(path, expected string) error {
	actual, err := setup_version(path)
	if err != nil {
		return err
	}
	return match_artifact_version(actual, expected)
}
