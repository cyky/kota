package gatewayconfig

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	// EnvKey holds the key of a gateway given as a URL.
	EnvKey = "KOTA_GATEWAY_KEY"
	// EnvKeyFile holds the path of a file containing that key.
	EnvKeyFile = "KOTA_GATEWAY_KEY_FILE"
	// NoKey is the key source when none is configured.
	NoKey = "none"
)

// envHint tells the user how to set the key of a gateway given as a URL.
var envHint = fmt.Sprintf("set %s, or %s to a file containing the key", EnvKey, EnvKeyFile)

// envKey returns the key of a gateway given as a URL, from the
// environment, and its source.
func envKey(getenv func(string) string) (key, source string, err error) {
	key, file := getenv(EnvKey), getenv(EnvKeyFile)
	switch {
	case key != "" && file != "":
		return "", "", fmt.Errorf("both %s and %s are set; use one", EnvKey, EnvKeyFile)
	case file != "":
		key, err := readKey(file)
		if err != nil {
			return "", "", fmt.Errorf("gateway key: %w", err)
		}
		return key, EnvKeyFile + " " + file, nil
	case key != "":
		if err := validateKey(key); err != nil {
			return "", "", fmt.Errorf("%s: %w", EnvKey, err)
		}
		return key, EnvKey, nil
	}
	return "", NoKey, nil
}

// expandKeyFile resolves a keyFile path from the config: absolute, or
// starting with ~/ for the home directory in $HOME.
func expandKeyFile(path string, getenv func(string) string) (string, error) {
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		home := getenv("HOME")
		if !filepath.IsAbs(home) {
			return "", fmt.Errorf("keyFile %s: HOME is not set to an absolute path", path)
		}
		return filepath.Join(home, rest), nil
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("keyFile %s: give an absolute path or one starting with ~/", path)
	}
	return path, nil
}

// readKey reads the key in file, without surrounding whitespace. Errors
// name the file, never the key.
func readKey(file string) (string, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if key == "" {
		return "", fmt.Errorf("key file %s is empty", file)
	}
	if err := validateKey(key); err != nil {
		return "", fmt.Errorf("key file %s: %w", file, err)
	}
	return key, nil
}

// validateKey allows printable ASCII without spaces, so the key can't
// break out of the Authorization header it is sent in. The error doesn't
// quote the key.
func validateKey(key string) error {
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return errors.New("key must be printable ASCII without spaces")
		}
	}
	return nil
}
