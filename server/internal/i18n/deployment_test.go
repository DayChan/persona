package i18n

import (
	"regexp"
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
	for name, x := range catalogIndexes() {
		forms := x.forms
		for i := range forms {
			f := &forms[i]
			for _, text := range catalogTexts(forms) {
				if got, want := f.match(text), f.pattern.FindStringSubmatch(text); !slices.Equal(got, want) {
					t.Fatalf("%s %s on %q: match %q, pattern %q", name, f.key, text, got, want)
				}
			}
			for _, text := range unknownTexts {
				if f.fits(text) {
					t.Errorf("%s %s fits %q: it would run its regexp on every unknown text", name, f.key, text)
				}
			}
		}
	}
}

// The index by first byte finds, for every text, the template trying them all in order finds, with the same
// submatches: on each catalog's own texts, each starting byte and the empty text. An unknown text is tried against a
// few templates, not all of them.
func TestTemplateIndexFindsTheSameTemplate(t *testing.T) {
	for name, x := range catalogIndexes() {
		texts := catalogTexts(x.forms)
		for c := range 256 {
			texts = append(texts, string(rune(c)), string([]byte{byte(c)})+" failed: 7")
		}
		for _, text := range texts {
			want, wantMatch := -1, []string(nil)
			for i := range x.forms {
				if m := x.forms[i].pattern.FindStringSubmatch(text); m != nil {
					want, wantMatch = i, m
					break
				}
			}
			got, gotMatch := -1, []string(nil)
			for _, n := range x.candidates(text) {
				if m := x.forms[n].match(text); m != nil {
					got, gotMatch = int(n), m
					break
				}
			}
			if got != want || !slices.Equal(gotMatch, wantMatch) {
				t.Fatalf("%s on %q: the index finds template %d %q, trying all finds %d %q", name, text, got, gotMatch, want, wantMatch)
			}
		}
		for _, text := range unknownTexts {
			if n, all := len(x.candidates(text)), len(x.forms); n > all/6 {
				t.Errorf("%s tries %d of its %d templates on %q", name, n, all, text)
			}
		}
	}
}

// Each template compiles its regexp the first time a text fits it, so a broken source would fail only then: compile
// every catalog's sources here, each with a group for each placeholder.
func TestEveryTemplateCompiles(t *testing.T) {
	sets := map[string][]deploymentTemplate{"extension-status": extensionStatusTemplates()}
	for name, x := range catalogIndexes() {
		sets[name] = x.forms
	}
	for name, forms := range sets {
		for _, f := range forms {
			rx, err := regexp.Compile(f.pattern.src)
			if err != nil {
				t.Errorf("%s %s: %v", name, f.key, err)
			} else if rx.NumSubexp() != len(f.names) {
				t.Errorf("%s %s: %d groups for %d placeholders in %q", name, f.key, rx.NumSubexp(), len(f.names), f.pattern.src)
			}
		}
	}
}

// Building an index compiles no regexp; a text compiles only those of the templates it fits, up to the one it matches
// (#2475).
func TestTemplatesCompileOnFirstFit(t *testing.T) {
	x := indexTemplates(makeDeploymentTemplates())
	compiled := func() (n int) {
		for i := range x.forms {
			if x.forms[i].pattern.rx != nil {
				n++
			}
		}
		return n
	}
	if n := compiled(); n != 0 {
		t.Fatalf("building the index compiled %d regexps", n)
	}
	text := T(ZhHans, "devices-deploy.install_failed", Args{"err": "disk full"})
	fits := 0
	for _, n := range x.candidates(text) {
		if x.forms[n].fits(text) {
			fits++
		}
		if x.forms[n].match(text) != nil {
			break
		}
	}
	if n := compiled(); n == 0 || n > fits {
		t.Fatalf("matching one text compiled %d regexps, for %d templates it fits", n, fits)
	}
}

var unknownTexts = []string{"ssh: connect to host example port 22: Connection refused", "connection reset by peer", "Ready"}

func catalogIndexes() map[string]*templateIndex {
	return map[string]*templateIndex{"deployment": deploymentTemplates(), "inbox": inboxTemplates(), "computer": computerTemplates()}
}

// catalogTexts are the texts a catalog's templates are tried on: the empty text, a number, the unknown texts, and each
// template filled in (a count, and opaque text, for each placeholder).
func catalogTexts(forms []deploymentTemplate) []string {
	texts := append([]string{"", "7"}, unknownTexts...)
	for _, f := range forms {
		texts = append(texts, strings.Join(f.literals, "7"))
	}
	return texts
}
