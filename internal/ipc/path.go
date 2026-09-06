package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/kristyancarvalho/argo/internal/xdg"
)

func DefaultSocketPath() (string, error) {
	runtimeDirectory, configured, err := xdg.EnvironmentDirectory("XDG_RUNTIME_DIR")
	if err != nil {
		return "", err
	}
	if configured {
		return filepath.Join(runtimeDirectory, "argo", "argod.sock"), nil
	}

	userID := os.Getuid()
	if userID < 0 {
		return "", fmt.Errorf("determine current user for runtime socket")
	}

	temporaryDirectory, err := xdg.TemporaryDirectory()
	if err != nil {
		return "", err
	}

	return filepath.Join(temporaryDirectory, "argo-"+strconv.Itoa(userID), "argod.sock"), nil
}
