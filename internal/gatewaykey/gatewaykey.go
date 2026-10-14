// Package gatewaykey loads the optional API key for the model gateway
// (llama-swap apiKeys) from the host environment. The key is never stored
// in this repo or in any file localllm writes.
package gatewaykey

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

const (
	// EnvKey holds the key itself.
	EnvKey = "LOCALLLM_GATEWAY_KEY"
	// EnvFile holds the path of a file containing the key.
	EnvFile = "LOCALLLM_GATEWAY_KEY_FILE"
)

// Hint tells the user how to configure the key.
var Hint = fmt.Sprintf("set %s, or %s to a file containing the key", EnvKey, EnvFile)

// Load returns the configured key, or "" when none is configured. getenv
// is os.Getenv outside tests.
func Load(getenv func(string) string) (string, error) {
	key, file := getenv(EnvKey), getenv(EnvFile)
	switch {
	case key != "" && file != "":
		return "", fmt.Errorf("both %s and %s are set; use one", EnvKey, EnvFile)
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("gateway key: %w", err)
		}
		key = strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("gateway key file %s is empty", file)
		}
		if err := validate(key); err != nil {
			return "", fmt.Errorf("gateway key file %s: %w", file, err)
		}
	case key != "":
		if err := validate(key); err != nil {
			return "", fmt.Errorf("%s: %w", EnvKey, err)
		}
	}
	return key, nil
}

// validate allows printable ASCII without spaces, so the key can't break
// out of the Authorization header it is sent in. The error doesn't quote
// the key.
func validate(key string) error {
	for _, r := range key {
		if r < 0x21 || r > 0x7e {
			return errors.New("key must be printable ASCII without spaces")
		}
	}
	return nil
}
