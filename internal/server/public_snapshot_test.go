package server

import "testing"

func setPublicSnapshotForTest(t testing.TB, app *App, snap *publicProxySnapshot) {
	t.Helper()
	app.proxyMu.Lock()
	app.setPublicSnapshotLocked(snap)
	app.proxyMu.Unlock()
}
