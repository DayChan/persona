package extmgr

import (
	"bytes"
	"context"
	"testing"
)

// A retained sign-in is data, not registration. Without an installed package there is no WeChat runtime,
// connector or channel; startup must keep the encrypted state for a later compatible installation.
func TestWeChatNeedsInstalledExtensionAndKeepsSignIn(t *testing.T) {
	h := newTestManager(t)
	h.store = newExtensionStore()
	ctx := context.Background()
	secret := []byte(`{"creds":{"token":"synthetic-token","bot_id":"example-bot","user_id":"example-user","base_url":"https://ilinkai.weixin.qq.com/"},"cursor":"synthetic-cursor","context":"synthetic-context"}`)
	if err := h.secretsFor("wechat").Save(ctx, secret); err != nil {
		t.Fatal(err)
	}
	startTestExtensions(t, h)
	if h.extensions.reg.Get("wechat") != nil || h.extensions.get("wechat") != nil {
		t.Fatal("WeChat registered without an installed extension")
	}
	if h.connectors.Get("wechat") != nil {
		t.Fatal("WeChat connector registered without an installed extension")
	}
	if _, found := h.extensionChannel("wechat"); found {
		t.Fatal("WeChat channel registered without an installed extension")
	}
	got, err := h.secretsFor("wechat").Load(ctx)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatal("startup changed the retained WeChat sign-in")
	}
}
