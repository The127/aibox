package hvserver

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"testing"

	winio "github.com/Microsoft/go-winio"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/hcs"
	"github.com/the127/aibox/internal/hvservice"
)

// grants records what VMs were let at, and taken away from.
type grants struct {
	granted, revoked []string
	grantErr         error
	revokeErr        error
	onGrant          func()
}

func (g *grants) disks() stateDisks {
	return stateDisks{
		grant: func(id, path string) error {
			if g.grantErr != nil {
				return g.grantErr
			}

			g.granted = append(g.granted, id+" "+path)

			if g.onGrant != nil {
				g.onGrant()
			}

			return nil
		},
		revoke: func(id, path string) error {
			if g.revokeErr != nil {
				return g.revokeErr
			}

			g.revoked = append(g.revoked, id+" "+path)

			return nil
		},
	}
}

// servedPipe is the server end of a pipe whose client, this test as well,
// has sent a request, as the service would have one before it takes a
// state disk.
func servedPipe(t *testing.T) (server net.Conn) {
	t.Helper()

	path, sddl := ownPipe(t)

	l, err := listen(path, sddl)
	require.NoError(t, err)
	t.Cleanup(func() { _ = l.Close() })

	dialed := make(chan error, 1)

	go func() {
		c, err := winio.DialPipeAccessImpLevel(t.Context(), path, windows.GENERIC_READ|windows.GENERIC_WRITE, winio.PipeImpLevelIdentification)
		if err != nil {
			_ = l.Close()
			dialed <- err

			return
		}

		t.Cleanup(func() { _ = c.Close() })
		dialed <- nil

		if err := hvservice.Write(c, hvservice.Request{Op: hvservice.OpStart}); err != nil {
			_ = c.Close()
		}
	}()

	server, acceptErr := l.Accept()
	require.NoError(t, <-dialed)
	require.NoError(t, acceptErr)
	t.Cleanup(func() { _ = server.Close() })

	var request hvservice.Request
	require.NoError(t, hvservice.Read(hvservice.NewReader(server), &request))

	return server
}

// pipeOf is the handle of the server end of the pipe.
func pipeOf(conn net.Conn) windows.Handle {
	return windows.Handle(conn.(interface{ Fd() uintptr }).Fd())
}

// aDisk is a file of the test's, held open as aibox holds its state disk:
// for its attributes only, sharing all, so that Hyper-V can open it too.
func aDisk(t *testing.T, dir string) (path string, handle uint64) {
	t.Helper()

	path = filepath.Join(dir, "state.ext4")
	require.NoError(t, os.WriteFile(path, make([]byte, 4096), 0o600))

	p, err := windows.UTF16PtrFromString(path)
	require.NoError(t, err)

	h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, 0, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = windows.CloseHandle(h) })

	return path, uint64(h)
}

func TestTakeLetsTheVMAtTheFileOfTheHandle(t *testing.T) {
	t.Parallel()

	// arrange
	pipe := servedPipe(t)
	path, handle := aDisk(t, t.TempDir())
	g := &grants{}

	// act
	disk, err := g.disks().take(pipeOf(pipe), "vm-1", handle)

	// assert
	require.NoError(t, err)

	want, err := os.Stat(path)
	require.NoError(t, err)
	got, err := os.Stat(disk.path)
	require.NoError(t, err)
	assert.True(t, os.SameFile(want, got), "%s is %s", disk.path, path)
	assert.NotContains(t, disk.path, `\\?\`, "a path as the Host Compute Service takes it")

	assert.Equal(t, []string{"vm-1 " + disk.path}, g.granted)
	assert.Empty(t, g.revoked)

	require.NoError(t, disk.revoke())
	assert.Equal(t, []string{"vm-1 " + disk.path}, g.revoked)
}

func TestTakeRefusesWhatIsNoFileOfTheClient(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	// itself is a handle of the folder or link at the path, not of what it
	// points at
	itself := func(t *testing.T, path string) uint64 {
		t.Helper()

		p, err := windows.UTF16PtrFromString(path)
		require.NoError(t, err)

		h, err := windows.CreateFile(p, windows.FILE_READ_ATTRIBUTES, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
		require.NoError(t, err)
		t.Cleanup(func() { _ = windows.CloseHandle(h) })

		return uint64(h)
	}

	tests := map[string]func(t *testing.T, pipe net.Conn) uint64{
		"a handle that is none": func(*testing.T, net.Conn) uint64 { return 0xdead0 },
		"a folder":              func(t *testing.T, _ net.Conn) uint64 { return itself(t, dir) },
		"the pipe itself":       func(_ *testing.T, pipe net.Conn) uint64 { return uint64(pipeOf(pipe)) },
		"a link": func(t *testing.T, _ net.Conn) uint64 {
			// a junction, which anyone may make, where a file link takes a
			// privilege
			link := filepath.Join(t.TempDir(), "link")
			out, err := exec.Command("cmd", "/c", "mklink", "/J", link, dir).CombinedOutput() //nolint:gosec // the test's own folders
			require.NoError(t, err, "%s", out)

			return itself(t, link)
		},
		"a file the client may not write": func(t *testing.T, _ net.Conn) uint64 {
			path, handle := aDisk(t, t.TempDir())
			denyWrite(t, path)

			return handle
		},
	}

	for name, handleOf := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// arrange
			pipe := servedPipe(t)
			handle := handleOf(t, pipe)
			g := &grants{}

			// act
			_, err := g.disks().take(pipeOf(pipe), "vm-1", handle)

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadRequest)
			assert.Empty(t, g.granted, "nothing was granted")
		})
	}
}

// denyWrite keeps whoever runs the test from writing the file.
func denyWrite(t *testing.T, path string) {
	t.Helper()

	me, err := user.Current()
	require.NoError(t, err)

	sid, err := windows.StringToSid(me.Uid)
	require.NoError(t, err)

	deny := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_GENERIC_WRITE,
		AccessMode:        windows.DENY_ACCESS,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}

	have, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	require.NoError(t, err)

	old, _, err := have.DACL()
	require.NoError(t, err)

	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{deny}, old)
	require.NoError(t, err)

	require.NoError(t, windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil))
}

func TestTakeRevokesAGrantToAPathThatChangedMeanwhile(t *testing.T) {
	t.Parallel()

	// arrange: the grant goes to the path, under which another file is
	// put as it is made, as someone racing the service would
	dir := t.TempDir()
	path, handle := aDisk(t, dir)

	other := filepath.Join(dir, "other.ext4")
	require.NoError(t, os.WriteFile(other, make([]byte, 4096), 0o600))

	pipe := servedPipe(t)
	g := &grants{onGrant: func() {
		require.NoError(t, os.Rename(path, filepath.Join(dir, "moved.ext4")))
		require.NoError(t, os.Rename(other, path))
	}}

	// act
	_, err := g.disks().take(pipeOf(pipe), "vm-1", handle)

	// assert
	assert.ErrorIs(t, err, hvservice.ErrBadRequest)
	assert.Len(t, g.granted, 1)
	assert.Equal(t, g.granted, g.revoked, "the grant is taken away again")
}

func TestTakeTellsAGrantThatFailed(t *testing.T) {
	t.Parallel()

	// arrange
	pipe := servedPipe(t)
	_, handle := aDisk(t, t.TempDir())
	g := &grants{grantErr: context.DeadlineExceeded}

	// act
	_, err := g.disks().take(pipeOf(pipe), "vm-1", handle)

	// assert
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Empty(t, g.revoked)
}

func TestHCSPathIsADriveOfThisMachineWithoutTheLongPrefix(t *testing.T) {
	t.Parallel()

	// act
	path, err := hcsPath(`\\?\C:\Users\me\.aibox\projects\x\state.ext4`)

	// assert
	require.NoError(t, err)
	assert.Equal(t, `C:\Users\me\.aibox\projects\x\state.ext4`, path)
}

func TestHCSPathRefusesWhatIsOnNoDriveOfThisMachine(t *testing.T) {
	t.Parallel()

	for name, final := range map[string]string{
		"another machine":        `\\?\UNC\server\share\state.ext4`,
		"a volume with no drive": `\\?\Volume{5c2ee6b0-5a8f-4b4a-9c55-0c2b5f7e8f10}\state.ext4`,
		"no path at all":         "",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			// act
			_, err := hcsPath(final)

			// assert
			assert.ErrorIs(t, err, hvservice.ErrBadRequest)
		})
	}
}

func TestRevokeTakesAPathThatIsGoneForRevoked(t *testing.T) {
	t.Parallel()

	// arrange: as a task's disk is, unlinked while its VM ran
	pipe := servedPipe(t)
	_, handle := aDisk(t, t.TempDir())
	g := &grants{}
	g.revokeErr = &hcs.Error{Op: "HcsRevokeVmAccess", HRESULT: 0x80070002}

	disk, err := g.disks().take(pipeOf(pipe), "vm-1", handle)
	require.NoError(t, err)

	// act
	err = disk.revoke()

	// assert
	assert.NoError(t, err)
}
