package host

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

func assertTeardownLocalized(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrTeardownTimeout) {
		t.Fatal("FAIL teardown sentinel identity", err)
	}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		want := i18n.T(l, "extension-status.teardown_timeout")
		if text := i18n.ExtensionStatusText(l, err.Error()); !strings.Contains(text, want) {
			t.Fatalf("FAIL canonical status localization %s: %s", l, text)
		}
		if text := i18n.Localize(err, l); !strings.Contains(text, want) {
			t.Fatalf("FAIL typed error localization %s: %s", l, text)
		}
		raw, _ := json.Marshal(map[string]any{"status": StateError, "error": err.Error()})
		var projected map[string]string
		if e := json.Unmarshal(i18n.ExtensionClientJSON(l, raw), &projected); e != nil || !strings.Contains(projected["error"], want) {
			t.Fatalf("FAIL client status localization %s: %v %v", l, projected, e)
		}
		if strings.Contains(projected["error_detail"], i18n.T(i18n.En, "extension-status.teardown_timeout")) {
			t.Fatalf("FAIL hub-owned English detail %s: %v", l, projected)
		}
	}
}

func TestTeardownFailureLocalizationAndIdentity(t *testing.T) {
	diagnostic := &rpc.Error{Code: "fixture_diagnostic", Data: json.RawMessage(`{"scoped":true}`), Message: "external diagnostic: operation refused"}
	for _, cause := range []error{nil, errGroupCompletionUnsupported, diagnostic, errors.Join(errGroupCompletionUnsupported, diagnostic)} {
		err := &teardownFailure{cause: cause}
		assertTeardownLocalized(t, err)
		if cause != nil && !errors.Is(err, cause) {
			t.Fatal("FAIL underlying cause identity")
		}
		if errors.Is(cause, diagnostic) {
			var coded *rpc.Error
			if !errors.As(err, &coded) || coded.Code != diagnostic.Code || string(coded.Data) != string(diagnostic.Data) {
				t.Fatal("FAIL coded diagnostic identity")
			}
		}
		for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
			summary, detail := i18n.ExtensionFailureText(l, err.Error(), "extension-status.failed")
			if errors.Is(cause, diagnostic) && detail != diagnostic.Message {
				t.Fatalf("FAIL external diagnostic lost: %q", detail)
			}
			if !errors.Is(cause, diagnostic) && detail != "" {
				t.Fatalf("FAIL owned diagnostic became raw detail: %q", detail)
			}
			if errors.Is(cause, errGroupCompletionUnsupported) && !strings.Contains(summary, i18n.T(l, "extension-status.group_completion_unsupported")) {
				t.Fatalf("FAIL unsupported-platform summary: %q", summary)
			}
			wrapped := i18n.T(i18n.En, "extension-status.host_failure", i18n.Args{"name": "Example", "err": err.Error()})
			wrappedSummary, wrappedDetail := i18n.ExtensionFailureText(l, wrapped, "extension-status.failed")
			if wrappedSummary != i18n.T(l, "extension-status.host_failure", i18n.Args{"name": "Example", "err": summary}) || wrappedDetail != detail {
				t.Fatalf("FAIL nested host status: %q / %q", wrappedSummary, wrappedDetail)
			}
		}
	}
	// The previously repaired configured timeout retains all language and error
	// identities even when transport failure makes remote execution uncertain.
	timeout := newCallTimeout("Example", "action", 400*time.Millisecond)
	timeout.cause = errors.Join(timeout.cause, context.DeadlineExceeded, rpc.ErrDropped)
	var coded *rpc.Error
	if !errors.Is(timeout, context.DeadlineExceeded) || !errors.Is(timeout, rpc.ErrDropped) || !errors.As(timeout, &coded) || coded.Code != extension.CodeTimeout {
		t.Fatal("FAIL configured timeout identities")
	}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		if i18n.Localize(timeout, l) != i18n.T(l, "extension-status.call_timeout", i18n.Args{"name": "Example", "method": "action", "duration": "400ms"}) {
			t.Fatal("FAIL configured timeout localization", l)
		}
	}
	t.Log("PASS typed/canonical/client teardown three-language projection, external detail and coded/sentinel identities")
}
