//go:build linux

package launch

import (
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

func TestSealedHoldsTheDataThatNoOneCanChange(t *testing.T) {
	// act
	file, err := sealed("kernel", []byte("the kernel"))

	// assert
	require.NoError(t, err)
	t.Cleanup(func() { _ = file.Close() })

	content, err := io.ReadAll(file)
	require.NoError(t, err)
	assert.Equal(t, "the kernel", string(content))

	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	require.NoError(t, err)
	assert.Equal(t, unix.F_SEAL_SEAL|unix.F_SEAL_SHRINK|unix.F_SEAL_GROW|unix.F_SEAL_WRITE, seals)

	writable, err := os.OpenFile("/proc/self/fd/"+strconv.Itoa(int(file.Fd())), os.O_RDWR, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = writable.Close() })

	_, err = writable.WriteAt([]byte("x"), 0)
	assert.ErrorIs(t, err, unix.EPERM)
}
