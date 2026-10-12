package session

import "testing"

func TestTheWatcherTakesAltVForCtrlVOnWindows(t *testing.T) {
	t.Parallel()

	watcher, ok := watchPaste(nil, func() {}).(*pasteWatcher)
	if !ok || !watcher.altV {
		t.Error("the watcher of Windows takes no Alt+V for Ctrl+V")
	}
}
