//go:build !darwin

package devicewatch

import (
	"context"
	"errors"
)

func startPlatformWatcher(_ context.Context, _ string) (<-chan Event, error) {
	return nil, errors.New("platform watcher is only implemented on darwin")
}
