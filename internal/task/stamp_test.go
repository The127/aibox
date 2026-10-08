package task_test

import (
	"bytes"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/task"
)

func fixedTime() time.Time {
	return time.Date(2026, 10, 8, 15, 4, 5, 0, time.Local)
}

func TestLogStampsEveryLine(t *testing.T) {
	// arrange
	var out bytes.Buffer

	log := task.NewLog(&out, "a1b2c3", fixedTime)
	host, vm := log.Writer("aibox"), log.Writer("vm")

	// act
	_, err := host.Write([]byte("one\ntw"))
	require.NoError(t, err)
	_, err = vm.Write([]byte("aibox: three\n\n"))
	require.NoError(t, err)
	_, err = host.Write([]byte("o\nlast"))
	require.NoError(t, err)
	require.NoError(t, vm.Close())
	require.NoError(t, host.Close())

	// assert
	assert.Equal(t, "15:04:05 aibox[a1b2c3]: one\n"+
		"15:04:05 vm[a1b2c3]: aibox: three\n"+
		"15:04:05 vm[a1b2c3]: \n"+
		"15:04:05 aibox[a1b2c3]: two\n"+
		"15:04:05 aibox[a1b2c3]: last\n", out.String())
}

func TestLogDropsTheLabelALineStartsWith(t *testing.T) {
	// arrange
	var out bytes.Buffer

	log := task.NewLog(&out, "a1b2c3", fixedTime)

	// act
	_, err := log.Writer("aibox").Write([]byte("aibox: the proxy stopped\n"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "15:04:05 aibox[a1b2c3]: the proxy stopped\n", out.String())
}

func TestLogWritesAWriteAfterCloseAtOnce(t *testing.T) {
	// arrange
	var out bytes.Buffer

	w := task.NewLog(&out, "a1b2c3", fixedTime).Writer("aibox")
	require.NoError(t, w.Close())

	// act
	_, err := w.Write([]byte("late"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "15:04:05 aibox[a1b2c3]: late\n", out.String())
}

func TestLogCutsALongLine(t *testing.T) {
	// arrange
	var out bytes.Buffer

	log := task.NewLog(&out, "a1b2c3", fixedTime)

	// act
	_, err := log.Writer("vm").Write([]byte(strings.Repeat("x", 10000) + "\nnext\n"))

	// assert
	require.NoError(t, err)
	assert.Equal(t, "15:04:05 vm[a1b2c3]: "+strings.Repeat("x", 4096)+"\n15:04:05 vm[a1b2c3]: next\n", out.String())
}

func TestLogKeepsTheLinesOfWritersThatWriteAtOnce(t *testing.T) {
	// arrange
	var out bytes.Buffer

	log := task.NewLog(&out, "a1b2c3", fixedTime)

	var wg sync.WaitGroup

	// act
	for _, label := range []string{"aibox", "vm"} {
		w := log.Writer(label)

		wg.Go(func() {
			for range 100 {
				_, _ = w.Write([]byte("a "))
				_, _ = w.Write([]byte("line\n"))
			}
		})
	}

	wg.Wait()

	// assert
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	assert.Len(t, lines, 200)

	for _, line := range lines {
		assert.Regexp(t, `^15:04:05 (aibox|vm)\[a1b2c3\]: a line$`, line)
	}
}
