package app

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerSettingsRender(t *testing.T) {
	m := initialModel(&fakeRepo{})
	m.settingsCursor = settingServerOn

	// Server off: one row, reading Off. The bind address and the token
	// configure an endpoint that is not running, so on a client — which is
	// most installs — they were setup for something the user already declined.
	m.syncCfg = syncConfig{}
	out := m.renderSettingsList()
	if !strings.Contains(out, "‹ Off ›") {
		t.Errorf("an idle server row should read Off; got:\n%s", out)
	}
	if !strings.Contains(out, "Make this machine the server") {
		t.Errorf("the Server page should say what it is for; got:\n%s", out)
	}
	for _, gone := range []string{"Listen address", "Token", "needs token"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q should be hidden while the server is off; got:\n%s", gone, out)
		}
	}

	// Running: the rows that describe the live endpoint come back, and the
	// token is masked (never plaintext).
	m.syncCfg = syncConfig{ServerToken: "hunter2-secret", ServerListen: "100.122.178.43:8765"}
	m.serverExternal = true
	out = m.renderSettingsList()
	if strings.Contains(out, "hunter2-secret") {
		t.Errorf("server token must be masked; got:\n%s", out)
	}
	for _, want := range []string{"Listen address", "Token", "100.122.178.43:8765", "external"} {
		if !strings.Contains(out, want) {
			t.Errorf("a running server should show %q; got:\n%s", want, out)
		}
	}
}

// Switching the server on with no token stored asks for the token and then
// completes the start, because the row holding the token is hidden while the
// server is off — refusing would point at something not on screen.
func TestServerToggleAsksForATokenThenStarts(t *testing.T) {
	if err := openStore(); err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := settingsModel(t)
	m.syncCfg = syncConfig{ServerListen: "127.0.0.1:0"}
	m.settingsCursor = settingServerOn
	m = sendKey(t, m, "enter")
	if m.mode != modeEditServerToken || !m.serverStartAfterToken {
		t.Fatalf("mode = %v, startAfterToken = %v; want the token editor", m.mode, m.serverStartAfterToken)
	}
	m = script(t, m, "a-strong-enough-server-token", "enter")
	if m.mode != modeNormal {
		t.Fatalf("mode = %v after saving the token", m.mode)
	}
	if m.inprocServer == nil {
		t.Fatal("saving the token should have completed the start the toggle asked for")
	}
	if m.serverStartAfterToken {
		t.Error("the pending-start flag must not survive the flow")
	}
	m.toggleServer() // leave no listener behind
}

func TestToggleServerNeedsToken(t *testing.T) {
	if err := openStore(); err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := initialModel(&fakeRepo{})
	m.syncCfg = syncConfig{} // no token

	m.toggleServer()
	if m.inprocServer != nil {
		t.Errorf("server must not start without a token")
		_ = m.inprocServer.Close()
	}
	if !strings.Contains(m.syncStatus, "token") {
		t.Errorf("expected a 'set a server token' hint, got %q", m.syncStatus)
	}
}

func TestToggleServerLifecycleAndServes(t *testing.T) {
	if err := openStore(); err != nil {
		t.Fatalf("openStore: %v", err)
	}
	m := initialModel(&fakeRepo{})
	m.syncCfg = syncConfig{ServerToken: "tok", ServerListen: "127.0.0.1:0"} // OS-assigned port

	m.toggleServer()
	if m.inprocServer == nil {
		t.Fatalf("server should start once a token is set (status: %q)", m.syncStatus)
	}
	if !m.syncCfg.ServerOn {
		t.Errorf("ServerOn should be persisted true")
	}

	// It actually answers health on the bound address.
	addr := m.inprocServer.Addr
	var ok bool
	for i := 0; i < 20; i++ {
		resp, err := (&http.Client{Timeout: time.Second}).Get("http://" + addr + "/v1/health")
		if err == nil {
			resp.Body.Close()
			ok = resp.StatusCode == http.StatusOK
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if !ok {
		t.Errorf("in-process server did not answer health on %s", addr)
	}

	// Toggling again stops it.
	m.toggleServer()
	if m.inprocServer != nil || m.syncCfg.ServerOn {
		t.Errorf("second toggle should stop the server and clear ServerOn")
	}
}
