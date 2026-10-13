//go:build linux

package vsockns

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeKernel is a kernel of the release whose sysctl folder of vsock is there or not, and
// whose list of modules has the lines, or is missing for nil.
func fakeKernel(t *testing.T, release string, sysctl bool, modules []string) kernel {
	t.Helper()

	root := t.TempDir()
	k := kernel{sysctl: filepath.Join(root, "vsock"), modules: filepath.Join(root, "modules"), release: release}

	if sysctl {
		require.NoError(t, os.Mkdir(k.sysctl, 0o700))
	}

	if modules != nil {
		dir := filepath.Join(k.modules, k.release)
		require.NoError(t, os.MkdirAll(dir, 0o700))

		var list string
		for _, line := range modules {
			list += line + "\n"
		}

		require.NoError(t, os.WriteFile(filepath.Join(dir, "modules.dep"), []byte(list), 0o600))
	}

	return k
}

func TestNoNamespacesTellsWhyAndWhatHelps(t *testing.T) {
	tests := map[string]struct {
		kernel    kernel
		want      string
		notLoaded bool
	}{
		"a kernel before 7.0": {
			kernel: fakeKernel(t, "6.19.3", true, nil),
			want:   "they came with Linux 7.0, and this kernel is 6.19.3",
		},
		"a kernel before 7.0 without vsock loaded": {
			kernel: fakeKernel(t, "6.8.0-1017-azure", false, []string{"kernel/drivers/vhost/vhost_vsock.ko.zst:"}),
			want:   "they came with Linux 7.0, and this kernel is 6.8.0-1017-azure",
		},
		"vsock as a module that is not loaded": {
			kernel: fakeKernel(t, "7.0.1", false, []string{
				"kernel/net/vmw_vsock/vsock.ko.zst:",
				"kernel/drivers/vhost/vhost_vsock.ko.zst: kernel/net/vmw_vsock/vsock.ko.zst kernel/drivers/vhost/vhost.ko.zst",
			}),
			want:      "vsock is not loaded. Load it with: sudo modprobe vhost_vsock",
			notLoaded: true,
		},
		"vsock as a module compressed with xz": {
			kernel:    fakeKernel(t, "7.0.1", false, []string{"kernel/drivers/vhost/vhost_vsock.ko.xz: kernel/net/vmw_vsock/vsock.ko.xz"}),
			want:      "Load it with: sudo modprobe vhost_vsock",
			notLoaded: true,
		},
		"a kernel without vsock": {
			kernel: fakeKernel(t, "7.0.1", false, []string{"kernel/drivers/vhost/vhost_net.ko:"}),
			want:   "the kernel was built without vsock. It needs CONFIG_VSOCKETS and CONFIG_VHOST_VSOCK",
		},
		"no list of modules": {
			kernel:    fakeKernel(t, "7.0.1", false, nil),
			want:      "vsock is not loaded. If the kernel has it as a module, load it with: sudo modprobe vhost_vsock. Otherwise the kernel needs CONFIG_VSOCKETS and CONFIG_VHOST_VSOCK",
			notLoaded: true,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			// act
			err := test.kernel.noNamespaces()

			// assert
			require.ErrorIs(t, err, ErrNoNamespaces)
			assert.ErrorContains(t, err, test.want)
			assert.Equal(t, test.notLoaded, errors.Is(err, ErrNotLoaded))
		})
	}
}
