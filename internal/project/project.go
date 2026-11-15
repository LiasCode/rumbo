// Package project answers where the project a directory belongs to starts.
package project

import (
	"context"
	"strings"
	"time"

	"github.com/LiasCode/rumbo/internal/tool"
)

// Root returns the directory that identifies the project dir is part of: the
// top of its git repository, or dir itself when it is not in one. Settings
// and sessions belong to the root, so they are found from any subdirectory.
func Root(ctx context.Context, dir string) string {
	if root := strings.TrimSpace(git(ctx, dir, "git rev-parse --show-toplevel 2>/dev/null")); root != "" {
		return root
	}

	return dir
}

func git(ctx context.Context, dir, command string) string {
	output, _ := tool.Shell(ctx, dir, command, 10*time.Second)

	return output
}
