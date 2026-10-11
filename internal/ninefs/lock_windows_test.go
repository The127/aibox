package ninefs_test

import (
	"context"
	"testing"
	"time"

	"github.com/hugelgupf/p9/p9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/the127/aibox/internal/ninefs"
)

// direct is the root of a share of the folder with no wire between, for
// what the p9 library refuses before the server sees it.
func direct(t *testing.T, dir string) p9.File {
	t.Helper()

	root, err := newFS(t, ninefs.Share{Name: "project", Dir: dir}).Attach()
	require.NoError(t, err)

	return walk(t, root, "project")
}

// opened walks to the names and opens the file, as a process of the guest
// does before it locks.
func opened(t *testing.T, root p9.File, names ...string) p9.File {
	t.Helper()

	f := walk(t, root, names...)

	_, _, err := f.Open(p9.ReadWrite)
	require.NoError(t, err)

	return f
}

func TestAWriteLockOfOneProcessBlocksAnother(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "src", "main.go")

	status, err := first.Lock(1, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)
	require.Equal(t, p9.LockStatusOK, status)

	// act
	blocked, err := second.Lock(2, p9.WriteLock, 0, 0, 0, "guest")

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.LockStatusBlocked, blocked)
}

func TestAnUnlockLetsTheOtherProcessIn(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "src", "main.go")

	_, err := first.Lock(1, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)

	_, err = first.Lock(1, p9.Unlock, 0, 0, 0, "guest")
	require.NoError(t, err)

	// act
	status, err := second.Lock(2, p9.WriteLock, 0, 0, 0, "guest")

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.LockStatusOK, status)
}

func TestALockHoldsForEveryNameOfTheFile(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	require.NoError(t, root.Link(walk(t, root, "src", "main.go"), "linked.go"))

	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "linked.go")

	_, err := first.Lock(1, p9.WriteLock, 0, 10, 0, "guest")
	require.NoError(t, err)

	// act
	status, err := second.Lock(2, p9.ReadLock, 0, 15, 1, "guest")

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.LockStatusBlocked, status)
}

func TestGetLockNamesTheLockInTheWay(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "src", "main.go")

	_, err := first.Lock(1, p9.WriteLock, 0, 10, 20, "guest")
	require.NoError(t, err)

	// act
	typ, start, length, pid, client, err := second.(p9.LockGetter).GetLock(2, p9.ReadLock, 0, 0, "guest")

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.WriteLock, typ)
	assert.Equal(t, uint64(10), start)
	assert.Equal(t, uint64(20), length)
	assert.Equal(t, 1, pid)
	assert.Equal(t, "guest", client)
}

func TestGetLockAnswersUnlockWhenNothingIsInTheWay(t *testing.T) {
	t.Parallel()

	// arrange: the owner's own lock is never in its way
	root := mount(t, tree(t))
	f := opened(t, root, "src", "main.go")

	_, err := f.Lock(1, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)

	// act
	typ, start, length, _, _, err := f.(p9.LockGetter).GetLock(1, p9.WriteLock, 5, 1, "guest")

	// assert
	require.NoError(t, err)
	assert.Equal(t, p9.Unlock, typ)
	assert.Equal(t, uint64(5), start)
	assert.Equal(t, uint64(1), length)
}

func TestABlockingLockWaitsUntilTheOtherProcessUnlocks(t *testing.T) {
	t.Parallel()

	// arrange
	root := mount(t, tree(t))
	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "src", "main.go")

	_, err := first.Lock(1, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)

	got := make(chan p9.LockStatus, 1)

	go func() {
		status, _ := second.Lock(2, p9.WriteLock, p9.LockFlagsBlock, 0, 0, "guest")
		got <- status
	}()

	// a lock that did not wait would answer Blocked at once
	select {
	case status := <-got:
		t.Fatalf("the blocking lock answered %s while the other lock was held", status)
	case <-time.After(200 * time.Millisecond):
	}

	// act
	_, err = first.Lock(1, p9.Unlock, 0, 0, 0, "guest")
	require.NoError(t, err)

	// assert
	select {
	case status := <-got:
		assert.Equal(t, p9.LockStatusOK, status)
	case <-time.After(10 * time.Second):
		t.Fatal("the blocking lock still waits after the unlock")
	}
}

func TestAWaitingLockEndsWithItsContextAndTakesNothing(t *testing.T) {
	t.Parallel()

	// arrange
	root := direct(t, tree(t))
	first := opened(t, root, "src", "main.go")
	second := opened(t, root, "src", "main.go")

	_, err := first.Lock(1, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(t.Context())
	got := make(chan p9.LockStatus, 1)

	go func() {
		status, _ := second.(p9.ContextLocker).LockContext(ctx, 2, p9.WriteLock, p9.LockFlagsBlock, 0, 0, "guest")
		got <- status
	}()

	// a lock that did not wait would answer Blocked at once
	select {
	case status := <-got:
		t.Fatalf("the blocking lock answered %s while the other lock was held", status)
	case <-time.After(200 * time.Millisecond):
	}

	// act
	cancel()

	// assert
	select {
	case status := <-got:
		assert.Equal(t, p9.LockStatusBlocked, status)
	case <-time.After(10 * time.Second):
		t.Fatal("the lock still waits after its context ended")
	}

	_, err = first.Lock(1, p9.Unlock, 0, 0, 0, "guest")
	require.NoError(t, err)

	third, err := second.Lock(3, p9.WriteLock, 0, 0, 0, "guest")
	require.NoError(t, err)
	assert.Equal(t, p9.LockStatusOK, third, "the lock whose context ended holds the file")
}
