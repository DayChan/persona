package i18n

import (
	"slices"
	"strings"
	"testing"
)

func TestDeploymentText(t *testing.T) {
	for _, l := range []Lang{ZhHans, En, Ja} {
		for key, source := range catalogs()[ZhHans] {
			if !strings.HasPrefix(key, "devices-deploy.") && !strings.HasPrefix(key, "hub-machine.deploy_") {
				continue
			}
			args := Args{"os": "example-os", "arch": "example-arch", "version": "example-build", "err": "disk failed\nextra detail", "why": "connection refused", "wait": T(ZhHans, "devices-deploy.minutes", Args{"count": 2}), "count": 2}
			translated := Args{}
			for k, v := range args {
				translated[k] = v
			}
			translated["wait"] = T(l, "devices-deploy.minutes", Args{"count": 2})
			base := key
			if strings.HasSuffix(key, "_other") {
				base = strings.TrimSuffix(key, "_other")
			}
			original := T(ZhHans, key, args)
			want := T(l, base, translated)
			if got := DeploymentText(l, original); got != want {
				t.Errorf("%s %s (%q): got %q want %q", l, key, source, got, want)
			}
		}
	}
	for _, sourceLang := range []Lang{ZhHans, En, Ja} {
		for _, targetLang := range []Lang{ZhHans, En, Ja} {
			for _, count := range []int{0, 1, 2, 90} {
				old := T(sourceLang, "devices-deploy.not_back_why", Args{"wait": T(sourceLang, "devices-deploy.seconds", Args{"count": count}), "why": T(sourceLang, "devices-deploy.node_temporary")})
				want := T(targetLang, "devices-deploy.not_back_why", Args{"wait": T(targetLang, "devices-deploy.seconds", Args{"count": count}), "why": T(targetLang, "devices-deploy.node_temporary")})
				if got := DeploymentText(targetLang, old); got != want {
					t.Errorf("%s -> %s count %d: got %q want %q", sourceLang, targetLang, count, got, want)
				}
			}
		}
	}
}

func TestDeploymentTextPreservesUnknownAndData(t *testing.T) {
	for _, text := range []string{"", "unknown node error", "用户自己的中文错误", "1 分钟后请重试", "prefix: hub 所在的机器跟着 hub 一起更新"} {
		if got := DeploymentText(En, text); got != text {
			t.Errorf("changed unknown text %q to %q", text, got)
		}
	}
	version := T(ZhHans, "devices-deploy.node_temporary")
	source := T(ZhHans, "devices-deploy.still_old", Args{"version": version})
	want := T(En, "devices-deploy.still_old", Args{"version": version})
	if got := DeploymentText(En, source); got != want {
		t.Fatalf("version was translated: %q", got)
	}
}

// A template's literals only skip texts its pattern cannot match: on its own catalog's texts (each template filled in)
// match is the pattern's answer. A text no template is worded as, the usual node or SSH error, fits none of them, so
// it runs no regexp.
func TestTemplateLiteralsOnlySkipMisses(t *testing.T) {
	unknown := []string{"ssh: connect to host example port 22: Connection refused", "connection reset by peer", "Ready"}
	for name, forms := range map[string][]deploymentTemplate{"deployment": deploymentTemplates(), "inbox": inboxTemplates(),
		"computer": computerTemplates()} {
		texts := append([]string{"", "7"}, unknown...)
		for _, f := range forms {
			texts = append(texts, strings.Join(f.literals, "7")) // a count, and opaque text, for each placeholder
		}
		for i := range forms {
			f := &forms[i]
			for _, text := range texts {
				if got, want := f.match(text), f.pattern.FindStringSubmatch(text); !slices.Equal(got, want) {
					t.Fatalf("%s %s on %q: match %q, pattern %q", name, f.key, text, got, want)
				}
			}
			for _, text := range unknown {
				if f.fits(text) {
					t.Errorf("%s %s fits %q: it would run its regexp on every unknown text", name, f.key, text)
				}
			}
		}
	}
}
