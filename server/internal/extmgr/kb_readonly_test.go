package extmgr

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

func TestKBReadOnlyBlocksExternalSourcesAndPluginWork(t *testing.T) {
	h := newTestManager(t)
	h.hello.Store(&assistantwire.Hello{KB: &assistantwire.KBSettings{ReadOnly: true}})
	ctx := context.Background()
	for _, method := range []string{assistantwire.ConnectorsArchives, assistantwire.ConnectorsChanges,
		assistantwire.ConnectorsArchiveMedia, assistantwire.ConnectorsItems, assistantwire.ConnectorsDocTitle, assistantwire.ConnectorsOwner,
		assistantwire.ConnectorsHistoryChats, assistantwire.ConnectorsHistory, assistantwire.ConnectorsHistoryAnchors} {
		_, handled, err := h.kbSourcesCall(ctx, assistantwire.AppCall{AppID: assistantwire.AppConnectors, Method: method})
		if !handled || !errors.Is(err, kb.ErrReadOnly) {
			t.Fatalf("%s: handled=%v err=%v", method, handled, err)
		}
	}
	for _, method := range []string{assistantwire.MethodKBPostsPut, assistantwire.MethodKBPostsComments,
		assistantwire.MethodKBPostsPending, assistantwire.MethodKBArchivedChats} {
		err := (kbPosts{h: h, id: "feed"}).call(ctx, method, assistantwire.KBPosts{}, nil)
		if !errors.Is(err, kb.ErrReadOnly) {
			t.Fatalf("%s: %v", method, err)
		}
	}
}
