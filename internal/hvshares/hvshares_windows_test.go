package hvshares

import (
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Microsoft/go-winio/pkg/guid"
	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/hvchild"
	"github.com/the127/aibox/internal/ninefs"
)

// helper runs serve over pipes in memory, with a listener of the loopback
// for the port of the VM, and returns aibox's side of it, the address the
// guest dials, and what serve returned once it did.
func helper(t *testing.T) (aibox *hvchild.Conn, answers <-chan string, addr <-chan string, done <-chan error) {
	t.Helper()

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()

	addrs := make(chan string, 1)
	served := make(chan error, 1)

	go func() {
		served <- serve(inR, outW, func(guid.GUID, uint32) (net.Listener, error) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err == nil {
				addrs <- l.Addr().String()
			}

			return l, err
		})

		_ = outW.Close()
	}()

	got := make(chan string, 4)
	aibox = hvchild.New(outR, inW, 0, inW)

	go func() {
		_ = aibox.Serve(func(kind string, body json.RawMessage) { got <- kind + " " + string(body) })
	}()

	t.Cleanup(func() { _ = inW.Close() })

	return aibox, got, addrs, served
}

func TestTheHelperServesTheSharesItIsGivenUntilAiboxEnds(t *testing.T) {
	// arrange
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README"), []byte("hi"), 0o600))

	aibox, answers, addrs, done := helper(t)

	// act
	require.NoError(t, aibox.Send(kindConfig, Config{Port: 564, Shares: []ninefs.Share{{Name: "project", Dir: dir}}}))

	// assert
	assert.Equal(t, "ready null", <-answers)

	conn, err := net.Dial("tcp", <-addrs)
	require.NoError(t, err)

	client, err := p9.NewClient(conn)
	require.NoError(t, err)

	root, err := client.Attach("project")
	require.NoError(t, err)

	_, f, err := root.Walk([]string{"README"})
	require.NoError(t, err, "the share's files")
	require.NoError(t, f.Close())
	require.NoError(t, client.Close())

	require.NoError(t, aibox.Close())

	select {
	case err := <-done:
		assert.NoError(t, err, "the end of aibox is the helper's end")
	case <-time.After(10 * time.Second):
		t.Fatal("the helper outlived aibox")
	}
}

func TestTheHelperSaysWhyItCantServe(t *testing.T) {
	// arrange
	aibox, answers, _, done := helper(t)

	// act
	require.NoError(t, aibox.Send(kindConfig, Config{Port: 564, Shares: []ninefs.Share{{Name: "project", Dir: filepath.Join(t.TempDir(), "none")}}}))

	// assert
	answer := <-answers
	assert.Contains(t, answer, kindFailed)
	assert.Contains(t, answer, "none")
	assert.NoError(t, <-done, "aibox says why, not the helper as well")
}

func TestTheHelperEndsWhenAiboxEndsBeforeTheConfig(t *testing.T) {
	// arrange
	aibox, _, _, done := helper(t)

	// act
	require.NoError(t, aibox.Close())

	// assert
	assert.NoError(t, <-done)
}
