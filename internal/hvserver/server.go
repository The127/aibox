package hvserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/the127/aibox/internal/hvservice"
)

// run serves the connections of l until ctx ends, and returns once every
// VM of them has ended. who tells the client of a connection. If l fails,
// run ends the VMs as well: a service that takes no connections can't
// serve, and would only keep VMs whose clients could not reach it again.
func (s *server) run(ctx context.Context, l net.Listener, who func(net.Conn) (hvservice.Client, error)) error {
	var wg sync.WaitGroup
	defer wg.Wait()

	ctx, end := context.WithCancel(ctx)
	defer end()

	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()

	for {
		conn, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}

			return fmt.Errorf("take a connection: %w", errors.Join(err, l.Close()))
		}

		wg.Go(func() {
			defer func() { _ = conn.Close() }()

			// a client may wait as long as its VM runs, so the service
			// stops its read by closing its connection
			stopConn := context.AfterFunc(ctx, func() { _ = conn.Close() })
			defer stopConn()

			s.serve(ctx, conn, func() (hvservice.Client, error) { return who(conn) })
		})
	}
}
