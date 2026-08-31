package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func DefaultSocketPath() (string, error) {
	if runtimeDirectory := os.Getenv("XDG_RUNTIME_DIR"); runtimeDirectory != "" {
		return filepath.Join(runtimeDirectory, "argo", "argod.sock"), nil
	}

	userID := os.Getuid()
	if userID < 0 {
		return "", fmt.Errorf("determine current user for runtime socket")
	}

	return filepath.Join(os.TempDir(), "argo-"+strconv.Itoa(userID), "argod.sock"), nil
}
