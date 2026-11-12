package config

import (
	"os"
	"path/filepath"
	"strings"
)

const GatewayFilename = "config.yaml"

func ExecutableConfig(name string) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(executable), "config", name), nil
}

func portableGatewayConfig(path string) bool {
	return strings.EqualFold(filepath.Base(path), GatewayFilename) &&
		strings.EqualFold(filepath.Base(filepath.Dir(path)), "config")
}
