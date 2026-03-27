//go:build darwin

package devicewatch

import (
	"context"
	"fmt"
	"syscall"
)

func startPlatformWatcher(ctx context.Context, dev string) (<-chan Event, error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return nil, fmt.Errorf("open route socket: %w", err)
	}

	out := make(chan Event, 4)
	go func() {
		defer close(out)
		defer syscall.Close(fd)

		// Ensure blocking read exits when context is cancelled.
		go func() {
			<-ctx.Done()
			_ = syscall.Close(fd)
		}()

		present := interfaceExists(dev)
		select {
		case out <- Event{Dev: dev, Present: present, Type: EventInitial}:
		case <-ctx.Done():
			return
		}

		buf := make([]byte, 8192)
		for {
			n, err := syscall.Read(fd, buf)
			if err != nil {
				// Expected when fd is closed due to context cancellation.
				return
			}
			if n <= 0 {
				continue
			}

			nowPresent := interfaceExists(dev)
			if nowPresent == present {
				continue
			}
			present = nowPresent

			evType := EventDisappeared
			if nowPresent {
				evType = EventAppeared
			}
			select {
			case out <- Event{Dev: dev, Present: nowPresent, Type: evType}:
			case <-ctx.Done():
				return
			}
		}
	}()

	return out, nil
}
