package hub

import (
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

func TestModelRefusalNoticeLocalization(t *testing.T) {
	for _, subtype := range []string{"model_refusal_fallback", "model_refusal_no_fallback", "model_refusal_future"} {
		t.Run(subtype, func(t *testing.T) {
			raw := `{"type":"system","subtype":"` + subtype + `","uuid":"refusal-example","trigger":"refusal","direction":"retry","scope":"session","originalModel":"primary-model","fallbackModel":"fallback-model","requestId":"synthetic-request","apiRefusalCategory":"synthetic-policy","apiRefusalExplanation":"Upstream refused the request","refusedUserMessageUuid":"synthetic-user","retractedMessageUuids":[]}`
			var meta transcript.Meta
			items := transcript.ParseLine(1, []byte(raw), &meta)
			if len(items) != 1 || items[0].Kind != proto.KindNotice || items[0].ModelRefusal == nil {
				t.Fatalf("refusal notice missing: %+v", items)
			}
			original := items[0]
			for lang, sourceLabel := range map[i18n.Lang]string{
				i18n.En: "Original record:", i18n.ZhHans: "原始记录：", i18n.Ja: "元の記録：",
			} {
				got := localizedItems(lang, items)[0]
				if !strings.Contains(got.Text, sourceLabel) || !strings.HasSuffix(got.Text, "\n"+raw) {
					t.Fatalf("%s: missing explanation or original record: %q", lang, got.Text)
				}
				if got.ModelRefusal != original.ModelRefusal || got.ID != original.ID || got.Seq != original.Seq {
					t.Fatalf("%s: changed original data: %+v", lang, got)
				}
				if subtype == "model_refusal_fallback" && !strings.Contains(got.Text, "/model") {
					t.Fatalf("%s: no recovery guidance: %q", lang, got.Text)
				}
				if subtype != "model_refusal_fallback" && strings.Contains(got.Text, "/model") {
					t.Fatalf("%s: invented a fallback for %s", lang, subtype)
				}
			}
			if items[0].Text != original.Text || items[0].ModelRefusal.Raw != raw {
				t.Fatal("localization mutated the shared transcript")
			}
			if got := localizedItems(i18n.Lang("unknown"), items)[0]; got.Text != localizedItems(i18n.ZhHans, items)[0].Text {
				t.Fatal("an unknown language must use the default")
			}
		})
	}
}
