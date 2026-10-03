// Package tunnel accepts connections and joins pairs of them.
package tunnel

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"syscall"
	"time"
)

const (
	firstRetry = 5 * time.Millisecond
	maxRetry   = time.Second
)

// Serve accepts connections until the context ends and calls handle for each
// one in a goroutine of its own. The connection is closed when handle
// returns or when the context ends. Serve returns when every handler has
// returned.
func Serve(ctx context.Context, listener net.Listener, handle func(ctx context.Context, conn net.Conn)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()

	var handlers sync.WaitGroup
	defer handlers.Wait()

	retry := firstRetry

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			if isTransient(err) {
				wait(ctx, retry)
				retry = min(retry*2, maxRetry)

				continue
			}

			cancel()

			return fmt.Errorf("accept a connection: %w", err)
		}

		retry = firstRetry

		handlers.Add(1)

		go func() {
			defer handlers.Done()
			defer func() { _ = conn.Close() }()

			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stop()

			handle(ctx, conn)
		}()
	}
}

// Join copies bytes in both directions until both sides have stopped
// sending. A side that stops sending is passed on to the other side as a
// half close, or as a close when the connection has no half close.
func Join(a, b net.Conn) {
	var directions sync.WaitGroup

	directions.Add(2)

	go copyThenClose(&directions, a, b)
	go copyThenClose(&directions, b, a)

	directions.Wait()
}

func copyThenClose(directions *sync.WaitGroup, dst, src net.Conn) {
	defer directions.Done()

	_, _ = io.Copy(dst, src)

	if closer, ok := dst.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()

		return
	}

	_ = dst.Close()
}

// isTransient says whether Accept may work again in a moment, as when the
// process is out of file descriptors.
func isTransient(err error) bool {
	for _, errno := range []syscall.Errno{syscall.EMFILE, syscall.ENFILE, syscall.ECONNABORTED, syscall.ECONNRESET} {
		if errors.Is(err, errno) {
			return true
		}
	}

	return false
}

func wait(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
