package appcontainer_test

import (
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows"

	"github.com/the127/aibox/internal/appcontainer"
)

// tokenIsAppContainer is TokenIsAppContainer, which x/sys/windows lacks.
const tokenIsAppContainer = 29

// cmdExe is a program every Windows has, which starts in a container.
const cmdExe = `C:\Windows\System32\cmd.exe`

// getHandleInformation is GetHandleInformation, which x/sys/windows lacks.
var getHandleInformation = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetHandleInformation")

// childVar makes the test binary the child, doing what it names.
const childVar = "APPCONTAINER_TEST_CHILD"

func TestMain(m *testing.M) {
	if what := os.Getenv(childVar); what != "" {
		os.Exit(child(what, os.Args[1:]))
	}

	os.Exit(m.Run())
}

// child is the test binary started in a container: it reports what it can
// do on its standard output.
func child(what string, args []string) int {
	switch what {
	case "container":
		var isContainer uint32

		var n uint32

		token := windows.GetCurrentProcessToken()
		if err := windows.GetTokenInformation(token, tokenIsAppContainer, (*byte)(unsafe.Pointer(&isContainer)), 4, &n); err != nil { //nolint:gosec // the value as the API takes it
			fmt.Println("error", err)

			return 1
		}

		fmt.Println("container", isContainer == 1)
	case "read":
		_, err := os.ReadFile(args[0]) //nolint:gosec // the file the test asks the child to try
		fmt.Println("read", err == nil)
	case "write-handle":
		h, _ := strconv.ParseUint(args[0], 10, 64)
		_, err := os.NewFile(uintptr(h), "inherited").WriteString("written\n")
		fmt.Println("wrote", err == nil)
	case "env":
		fmt.Println(strings.Join(os.Environ(), "\n"))
	case "console":
		con, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err == nil {
			_ = con.Close()
		}

		fmt.Println("console", err == nil)
	case "temp":
		f, err := os.CreateTemp("", "x")
		if err == nil {
			_ = f.Close()
			_ = os.Remove(f.Name())
		}

		fmt.Println("temp", err == nil)
	case "valid":
		// outside a container only, which ends a program that asks about a
		// handle that isn't its own
		h, _ := strconv.ParseUint(args[0], 10, 64)

		var flags uint32
		if r, _, _ := getHandleInformation.Call(uintptr(h), uintptr(unsafe.Pointer(&flags))); r != 0 { //nolint:gosec // a pointer as the API takes it
			return 10
		}

		return 11
	case "sleep":
		time.Sleep(time.Minute)
	case "exit":
		code, _ := strconv.Atoi(args[0])

		return code
	}

	return 0
}

// start starts the test binary as the child doing what, in a container
// unless container is nil, and returns what it printed and its exit code.
func start(t *testing.T, container *windows.SID, what string, args []string, inherit ...windows.Handle) (string, uint32) {
	t.Helper()

	job, err := appcontainer.NewJob()
	require.NoError(t, err)
	t.Cleanup(func() { _ = job.Close() })

	r, w, err := os.Pipe()
	require.NoError(t, err)

	self, err := os.Executable()
	require.NoError(t, err)

	internet, err := windows.StringToSid(appcontainer.InternetClient)
	require.NoError(t, err)

	var capabilities []*windows.SID
	if container != nil {
		capabilities = []*windows.SID{internet}
	}

	p, err := appcontainer.Start(appcontainer.Spec{
		Path:         self,
		Args:         append([]string{self}, args...),
		Env:          append(appcontainer.SystemEnv(), childVar+"="+what),
		Container:    container,
		Capabilities: capabilities,
		Stdout:       windows.Handle(w.Fd()),
		Stderr:       windows.Handle(w.Fd()),
		Inherit:      inherit,
		Job:          job,
	})
	require.NoError(t, err)

	_ = w.Close()

	out, err := io.ReadAll(r)
	require.NoError(t, err)

	code, err := p.Wait()
	require.NoError(t, err)
	require.NoError(t, p.Close())

	return strings.TrimSpace(string(out)), code
}

// aContainer is the SID of a container of its own for the test, with its
// profile.
func aContainer(t *testing.T) *windows.SID {
	t.Helper()

	name := "aibox.test." + rand.Text()

	sid, err := appcontainer.CreateProfile(name)
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, appcontainer.DeleteProfile(name)) })

	return sid
}

func TestAProgramRunsInTheContainer(t *testing.T) {
	// act
	out, code := start(t, aContainer(t), "container", nil)

	// assert
	assert.Equal(t, uint32(0), code)
	assert.Equal(t, "container true", out)
}

// Windows starts nothing in a container that has a SID but no profile,
// which is why a container needs one, though its SID depends only on its
// name.
func TestAContainerWithoutItsProfileStartsNothing(t *testing.T) {
	// arrange
	sid, err := appcontainer.DeriveSID("aibox.test." + rand.Text())
	require.NoError(t, err)

	job, err := appcontainer.NewJob()
	require.NoError(t, err)
	t.Cleanup(func() { _ = job.Close() })

	// act
	_, err = appcontainer.Start(appcontainer.Spec{Path: cmdExe, Args: []string{"cmd.exe", "/c", "exit"}, Env: appcontainer.SystemEnv(), Container: sid, Job: job})

	// assert
	assert.Error(t, err)
}

func TestTheProfileHasTheSIDTheNameDerives(t *testing.T) {
	// arrange
	name := "aibox.test." + rand.Text()

	// act
	made, err := appcontainer.CreateProfile(name)
	require.NoError(t, err)
	t.Cleanup(func() { _ = appcontainer.DeleteProfile(name) })

	again, againErr := appcontainer.CreateProfile(name)
	derived, deriveErr := appcontainer.DeriveSID(name)

	// assert
	require.NoError(t, againErr)
	require.NoError(t, deriveErr)
	assert.True(t, made.Equals(derived))
	assert.True(t, again.Equals(derived), "a profile that is there already")
}

func TestAProgramInTheContainerReadsNoFileOfTheUser(t *testing.T) {
	// arrange
	home, err := os.UserHomeDir()
	require.NoError(t, err)

	secret := filepath.Join(home, ".aibox-appcontainer-test-"+rand.Text())
	require.NoError(t, os.WriteFile(secret, []byte("secret"), 0o600))
	t.Cleanup(func() { _ = os.Remove(secret) })

	// act
	outside, _ := start(t, nil, "read", []string{secret})
	inside, _ := start(t, aContainer(t), "read", []string{secret})

	// assert
	assert.Equal(t, "read true", outside, "the user's own rights")
	assert.Equal(t, "read false", inside)
}

func TestAProgramInTheContainerWritesAHandleItIsGiven(t *testing.T) {
	// arrange
	path := filepath.Join(t.TempDir(), "log")
	f, err := os.Create(path) //nolint:gosec // the test's own
	require.NoError(t, err)

	// act
	out, _ := start(t, aContainer(t), "write-handle", []string{strconv.FormatUint(uint64(f.Fd()), 10)}, windows.Handle(f.Fd()))
	require.NoError(t, f.Close())

	// assert
	assert.Equal(t, "wrote true", out)

	written, err := os.ReadFile(path) //nolint:gosec // the test's own
	require.NoError(t, err)
	assert.Equal(t, "written\n", string(written))
}

func TestAProgramGetsNoHandleItIsNotGiven(t *testing.T) {
	// arrange: a handle that could be inherited, but is not on the list
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT))

	// act
	out, code := start(t, aContainer(t), "write-handle", []string{strconv.FormatUint(uint64(f.Fd()), 10)})

	// assert: a container checks handles strictly, and Windows ends the
	// program that uses one that is none of its own
	assert.NotContains(t, out, "wrote true")
	assert.NotEqual(t, uint32(0), code)
}

func TestAProgramGetsNoHandleWithoutAList(t *testing.T) {
	// arrange: a handle that could be inherited, and a program with no
	// handles to get, so no list
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	require.NoError(t, windows.SetHandleInformation(windows.Handle(f.Fd()), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT))

	job, err := appcontainer.NewJob()
	require.NoError(t, err)
	t.Cleanup(func() { _ = job.Close() })

	self, err := os.Executable()
	require.NoError(t, err)

	// act
	p, err := appcontainer.Start(appcontainer.Spec{
		Path: self,
		Args: []string{self, strconv.FormatUint(uint64(f.Fd()), 10)},
		Env:  append(appcontainer.SystemEnv(), childVar+"=valid"),
		Job:  job,
	})
	require.NoError(t, err)

	code, err := p.Wait()
	require.NoError(t, err)
	t.Cleanup(func() { _ = p.Close() })

	// assert
	assert.Equal(t, uint32(11), code, "the handle is none of the program's")
}

func TestTheHandlesOfAProgramAreInheritableOnlyWhileItStarts(t *testing.T) {
	// arrange
	f, err := os.Create(filepath.Join(t.TempDir(), "log"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })

	// act
	start(t, aContainer(t), "write-handle", []string{strconv.FormatUint(uint64(f.Fd()), 10)}, windows.Handle(f.Fd()))

	// assert
	var flags uint32
	r, _, _ := getHandleInformation.Call(f.Fd(), uintptr(unsafe.Pointer(&flags))) //nolint:gosec // a pointer as the API takes it
	require.NotZero(t, r)
	assert.Zero(t, flags&windows.HANDLE_FLAG_INHERIT)
}

func TestAProgramInTheContainerHasATemporaryFolder(t *testing.T) {
	// act
	out, _ := start(t, aContainer(t), "temp", nil)

	// assert
	assert.Equal(t, "temp true", out)
}

func TestAProgramGetsOnlyTheEnvironmentItIsGiven(t *testing.T) {
	// arrange
	t.Setenv("AIBOX_TEST_SECRET", "a token")

	// act
	out, _ := start(t, aContainer(t), "env", nil)

	// assert
	assert.NotContains(t, out, "a token")
	assert.Contains(t, out, childVar+"=env")
}

func TestTheExitCodeOfTheProgramComesBack(t *testing.T) {
	// act
	_, code := start(t, aContainer(t), "exit", []string{"7"})

	// assert
	assert.Equal(t, uint32(7), code)
}

func TestClosingTheJobEndsItsPrograms(t *testing.T) {
	// arrange
	job, err := appcontainer.NewJob()
	require.NoError(t, err)

	self, err := os.Executable()
	require.NoError(t, err)

	p, err := appcontainer.Start(appcontainer.Spec{
		Path: self, Args: []string{self},
		Env:       append(appcontainer.SystemEnv(), childVar+"=sleep"),
		Container: aContainer(t),
		Job:       job,
	})
	require.NoError(t, err)

	ended := make(chan struct{})

	go func() {
		_, _ = p.Wait()
		close(ended)
	}()

	// act
	require.NoError(t, job.Close())

	// assert
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		_ = p.Kill()

		t.Fatal("the program outlived its job")
	}
}

func TestStartRefusesAProgramWithoutAJob(t *testing.T) {
	// act
	_, err := appcontainer.Start(appcontainer.Spec{Path: "cmd.exe"})

	// assert
	assert.ErrorIs(t, err, appcontainer.ErrNoJob)
}

func TestAProgramInTheContainerHasNoConsole(t *testing.T) {
	// act
	out, _ := start(t, aContainer(t), "console", nil)

	// assert
	assert.Equal(t, "console false", out)
}
