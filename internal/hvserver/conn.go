package hvserver

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/Microsoft/go-winio/pkg/guid"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// vm is a VM the service made, as the Host Compute Service has it.
type vm interface {
	runtimeID(ctx context.Context) (guid.GUID, error)
	Start(ctx context.Context) error
	Terminate(ctx context.Context) error
	Close() error
}

// peer is the client on the other end of a connection, as the pipe tells
// it: who they are, the state disk they hand over by its handle, and the
// console pipe the service hands them.
type peer interface {
	client() (hvservice.Client, error)
	// stateDisk takes the state disk the client holds open under the
	// handle, lets the VM of the id at it, and tells its path.
	stateDisk(id string, handle uint64) (stateDisk, error)
	// console makes the pipe at name for the console of the client's VM.
	console(name string) (console, error)
}

// stateDisk is the state disk of a client as the VM gets it, and how to
// take the VM's access to it away again.
type stateDisk struct {
	path   string
	revoke func() error
}

// console is the pipe the service made for the console of a VM: give
// hands it to the client, and tells the handle in the client's process,
// and close lets go of the service's own handle, which the client's
// outlives.
type console struct {
	give  func() (uint64, error)
	close func() error
}

// server is what the service needs to make VMs, which the tests fake.
type server struct {
	install hvservice.Install
	ledger  ledger
	host    func() (hvservice.Host, error)
	create  func(ctx context.Context, id string, doc hcs.Document) (vm, error)
	logf    func(format string, args ...any)
	// containerSID is the SID of the AppContainer of the name, and network
	// lets the container of a SID reach the network, until the function it
	// returns is called
	containerSID func(name string) (string, error)
	network      func(sid string, n hvservice.Network) (func() error, error)
	// exempt lets the container of a SID reach the loopback, until the
	// function it returns is called
	exempt func(sid string) (func() error, error)
}

// errOutOfTurn is a request that comes when the VM of the connection
// can't do it: a second create, or a start before a create or after one.
var errOutOfTurn = fmt.Errorf("%w: out of turn", hvservice.ErrBadRequest)

// session is the VM of one connection, from its create to its end, and
// what its container may reach, until the connection ends.
type session struct {
	vm      vm
	disk    stateDisk
	release func()
	started bool
	// unnetwork take the network and the loopback away from the containers
	// of the connection's VMs, last first. They outlive the VM: aibox ends
	// the helper in the container after the VM, and the connection after
	// the helper, so that the helper is never left without its filters.
	unnetwork []func() error
}

// serve answers the requests of one connection, from the peer p, and
// ends its VM when the connection ends: a client that is gone can't stop
// its VM itself. It waits for a request as long as the client's VM runs,
// so that the service stops it by closing conn.
func (s *server) serve(ctx context.Context, conn io.ReadWriter, p peer) {
	r := hvservice.NewReader(conn)

	var sess session

	defer func() {
		s.end(ctx, &sess)
		s.unnetwork(&sess)
	}()

	for {
		// a new value, so that nothing of one request is taken for the next
		var request hvservice.Request

		err := hvservice.Read(r, &request)

		var reply hvservice.Reply

		switch {
		case err == nil:
			reply = s.answer(ctx, &sess, request, p)
		case errors.Is(err, hvservice.ErrBadMessage):
			reply = hvservice.Refusal(err)
		case errors.Is(err, hvservice.ErrMessageTooLong):
			// the rest of the line is still to come, so nothing after it
			// can be told from it
			s.send(conn, hvservice.Refusal(err))

			return
		default:
			// a client that hangs up, or the service that stops, ends a
			// read as it should
			if !errors.Is(err, io.EOF) && ctx.Err() == nil {
				s.logf("read from a client: %v", err)
			}

			return
		}

		if !s.send(conn, reply) {
			return
		}
	}
}

// answer does what the request asks of the VM of the connection, sess.
func (s *server) answer(ctx context.Context, sess *session, request hvservice.Request, p peer) hvservice.Reply {
	if err := request.Check(); err != nil {
		return hvservice.Refusal(err)
	}

	switch request.Op {
	case hvservice.OpCreate:
		if sess.vm != nil {
			return hvservice.Refusal(fmt.Errorf("%w: create of a second VM", errOutOfTurn))
		}

		reply, err := s.createVM(ctx, sess, *request.Create, p)
		if err != nil {
			s.logf("create a VM: %v", err)

			return refusal("create", err)
		}

		return reply
	case hvservice.OpStart:
		if sess.vm == nil || sess.started {
			return hvservice.Refusal(fmt.Errorf("%w: start of no VM or one that runs", errOutOfTurn))
		}

		if err := sess.vm.Start(ctx); err != nil {
			s.logf("start a VM: %v", err)

			// a VM that didn't start is of no further use; the client may
			// create another
			s.end(ctx, sess)

			return refusal("start", err)
		}

		sess.started = true
	case hvservice.OpStop:
		if sess.vm == nil {
			return hvservice.Refusal(fmt.Errorf("%w: stop of no VM", errOutOfTurn))
		}

		// answered once the VM is gone, which is what the client waits for
		s.end(ctx, sess)
	}

	return hvservice.Reply{}
}

// createVM makes the VM the client asked for, within what the host has
// left, with the state disk the peer hands over, and keeps it in sess.
func (s *server) createVM(ctx context.Context, sess *session, c hvservice.Create, p peer) (hvservice.Reply, error) {
	client, err := p.client()
	if err != nil {
		return hvservice.Reply{}, err
	}

	host, err := s.host()
	if err != nil {
		return hvservice.Reply{}, err
	}

	id, err := guid.NewV4()
	if err != nil {
		return hvservice.Reply{}, fmt.Errorf("make the id of the VM: %w", err)
	}

	// before the state disk, so that a VM the host can't take has no
	// access to grant and take away again
	if err := c.Check(host); err != nil {
		return hvservice.Reply{}, err
	}

	container, containerSID, err := s.container(client, c.Network)
	if err != nil {
		return hvservice.Reply{}, err
	}

	disk, err := p.stateDisk(id.String(), c.State)
	if err != nil {
		return hvservice.Reply{}, err
	}

	// before the VM, which Hyper-V would make a pipe of its own for; the
	// service's handle goes once the client has one, or the VM failed
	pipe := hvservice.ConsolePipe(id)

	con, err := p.console(pipe)
	if err != nil {
		s.revoke(disk)

		return hvservice.Reply{}, err
	}

	defer s.closeConsole(con)

	// the helper in the container listens on the Hyper-V sockets of the VM
	sids := client.SIDs()
	if containerSID != "" {
		sids = append(sids, containerSID)
	}

	doc, err := c.Document(host, s.install, pipe, disk.path, sids)
	if err != nil {
		s.revoke(disk)

		return hvservice.Reply{}, err
	}

	release, err := s.ledger.reserve(c.MemoryMiB, host.MemoryMiB)
	if err != nil {
		s.revoke(disk)

		return hvservice.Reply{}, err
	}

	v, err := s.create(ctx, id.String(), doc)
	if err != nil {
		s.revoke(disk)
		release()

		return hvservice.Reply{}, err
	}

	*sess = session{vm: v, disk: disk, release: release}

	// without its runtime id and its console the client can't reach the VM
	runtimeID, err := v.runtimeID(ctx)
	if err != nil {
		s.end(ctx, sess)

		return hvservice.Reply{}, err
	}

	// before the reply, after which aibox starts its helper in the container
	if c.Network != nil {
		// a create that fails has no helper, whose container goes at once
		fail := func(err error) (hvservice.Reply, error) {
			s.end(ctx, sess)
			s.unnetwork(sess)

			return hvservice.Reply{}, err
		}

		unfilter, err := s.network(containerSID, *c.Network)
		if err != nil {
			return fail(fmt.Errorf("filter the network of the container: %w", err))
		}

		sess.unnetwork = append(sess.unnetwork, unfilter)

		// Windows keeps a container from the loopback whatever the filters
		// let it, but for those on a list of the machine
		if len(c.Network.Loopback) > 0 {
			unexempt, err := s.exempt(containerSID)
			if err != nil {
				return fail(fmt.Errorf("let the container reach the loopback: %w", err))
			}

			sess.unnetwork = append(sess.unnetwork, unexempt)
		}

		// the counts, as the ports could be more than an event holds
		s.logf("the container %s of VM %s may connect to %d ports, and to %d of the loopback", container, id, len(c.Network.Ports), len(c.Network.Loopback))
	}

	handle, err := con.give()
	if err != nil {
		s.end(ctx, sess)

		return hvservice.Reply{}, err
	}

	s.logf("created VM %s for %s, with the state disk %s", id, client.User, disk.path)

	return hvservice.Reply{RuntimeID: runtimeID.String(), Console: handle, Container: container, ContainerSID: containerSID}, nil
}

// errContainedClient is a client in a container of its own that asks for
// the network of another, which may get more of it than its own has.
var errContainedClient = fmt.Errorf("%w: a client in an AppContainer asks for the network of another", hvservice.ErrBadRequest)

// container is the name and the SID of a container of its own for the VM,
// when the client asks for a network: the service names it, never after
// the request.
func (s *server) container(client hvservice.Client, n *hvservice.Network) (name, sid string, err error) {
	if n == nil {
		return "", "", nil
	}

	if client.AppContainer != "" {
		return "", "", errContainedClient
	}

	unique, err := guid.NewV4()
	if err != nil {
		return "", "", fmt.Errorf("name the container: %w", err)
	}

	name = hvservice.ContainerName(unique)

	if sid, err = s.containerSID(name); err != nil {
		return "", "", fmt.Errorf("find the SID of the container: %w", err)
	}

	return name, sid, nil
}

// closeConsole lets go of the service's handle of a console pipe.
func (s *server) closeConsole(con console) {
	if err := con.close(); err != nil {
		s.logf("let go of a console pipe: %v", err)
	}
}

// end ends the VM of the connection, if it made one, gives its memory
// back once it is gone, and leaves sess without a VM. It ends the VM also
// when ctx has ended, as the service stops.
func (s *server) end(ctx context.Context, sess *session) {
	if sess.vm == nil {
		return
	}

	ctx = context.WithoutCancel(ctx)

	if err := sess.vm.Terminate(ctx); err != nil {
		s.logf("end a VM: %v", err)
	}

	// the service's handle is the VM's only one, and its Document ends it
	// with its last handle, so the VM is gone whatever Terminate said
	if err := sess.vm.Close(); err != nil {
		s.logf("let go of a VM: %v", err)
	}

	s.revoke(sess.disk)
	sess.release()
	*sess = session{unnetwork: sess.unnetwork}
}

// unnetwork takes the network and the loopback away from the containers
// of the session, once their helpers are gone.
func (s *server) unnetwork(sess *session) {
	for i := len(sess.unnetwork) - 1; i >= 0; i-- {
		if err := sess.unnetwork[i](); err != nil {
			s.logf("take the network of a container away: %v", err)
		}
	}

	sess.unnetwork = nil
}

// revoke takes the VM's access to the state disk away, once the VM is
// gone or was never made.
func (s *server) revoke(disk stateDisk) {
	if err := disk.revoke(); err != nil {
		s.logf("take the VM's access to %s away: %v", disk.path, err)
	}
}

// send writes the reply, and tells whether the connection can go on.
func (s *server) send(conn io.Writer, reply hvservice.Reply) bool {
	if err := hvservice.Write(conn, reply); err != nil {
		s.logf("answer a client: %v", err)

		return false
	}

	return true
}
