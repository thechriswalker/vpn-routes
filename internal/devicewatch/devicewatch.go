package devicewatch

import (
	"context"
	"log/slog"
	"net"
	"time"
)

type EventType string

const (
	EventInitial     EventType = "initial"
	EventAppeared    EventType = "appeared"
	EventDisappeared EventType = "disappeared"
)

type Event struct {
	Dev     string
	Present bool
	Type    EventType
}

func Start(ctx context.Context, dev string, pollInterval time.Duration, logger *slog.Logger) <-chan Event {
	out := make(chan Event, 4)
	go func() {
		defer close(out)

		src, err := startPlatformWatcher(ctx, dev)
		if err != nil {
			logger.Warn("device event watcher unavailable; using polling fallback", "dev", dev, "err", err)
			src = startPollingWatcher(ctx, dev, pollInterval)
		}

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-src:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}

func startPollingWatcher(ctx context.Context, dev string, interval time.Duration) <-chan Event {
	out := make(chan Event, 4)
	go func() {
		defer close(out)

		present := interfaceExists(dev)
		select {
		case out <- Event{Dev: dev, Present: present, Type: EventInitial}:
		case <-ctx.Done():
			return
		}

		t := time.NewTicker(interval)
		defer t.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
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
		}
	}()
	return out
}

func interfaceExists(name string) bool {
	_, err := net.InterfaceByName(name)
	return err == nil
}
