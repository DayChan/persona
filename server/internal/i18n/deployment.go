package i18n

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// DeploymentText translates recognized deployment text at the response boundary. Deployment records and manager
// reports retain their original Chinese, including records written before i18n. Unknown node/SSH text is untouched.
// Only deployment catalogs are eligible: this is not a general reverse translation of user content or transcripts.
func DeploymentText(l Lang, text string) string { return deploymentText(l, text, 0) }

type deploymentTemplate struct {
	key     string
	pattern *templatePattern
	names   []string
	fixed   int
	// literals are the template's text around its placeholders, one more than names ("" where two placeholders meet
	// or one starts or ends it).
	literals []string
}

// fits: text holds the template's literal text where every match does (the pattern is anchored at both ends): it starts
// with the first literal, ends with the last and holds the others. Only a text that fits may match.
func (t *deploymentTemplate) fits(text string) bool {
	last := len(t.literals) - 1
	if !strings.HasPrefix(text, t.literals[0]) || !strings.HasSuffix(text, t.literals[last]) {
		return false
	}
	for _, l := range t.literals[1:max(1, last)] {
		if !strings.Contains(text, l) {
			return false
		}
	}
	return true
}

// templatePattern is a template's regexp, anchored at both ends, compiled from its source the first time it runs: a
// catalog has hundreds of templates in each language, a process meets the texts of few of them, and each compiled
// regexp costs a few kilobytes (#2475). TestEveryTemplateCompiles compiles them all. Safe for concurrent use.
type templatePattern struct {
	src  string
	once sync.Once
	rx   *regexp.Regexp
}

func (p *templatePattern) FindStringSubmatch(text string) []string {
	p.once.Do(func() { p.rx = regexp.MustCompile(p.src) })
	return p.rx.FindStringSubmatch(text)
}

// match is the pattern's submatches in text, nil when it does not match. A text is tried against a catalog's hundreds
// of templates in turn, and nearly all of them do not fit it: those cost a few string comparisons, not a regexp run,
// and a template no text has fitted yet has not compiled its regexp.
func (t *deploymentTemplate) match(text string) []string {
	if !t.fits(text) {
		return nil
	}
	return t.pattern.FindStringSubmatch(text)
}

// templateIndex is a catalog's templates in the order they are tried, indexed by the first byte of the texts each may
// match.
type templateIndex struct {
	forms []deploymentTemplate
	// first: for each byte a text may start with, the templates whose text starts with that byte or with a placeholder,
	// as indices into forms in their order. Any other template's first literal is not a prefix of such a text, so it
	// could not match (fits). open: those starting with a placeholder, the only ones that may match the empty text.
	first [256][]int32
	open  []int32
}

func indexTemplates(forms []deploymentTemplate) *templateIndex {
	x := &templateIndex{forms: forms}
	var lead [256][]int32
	for i := range forms {
		if l := forms[i].literals[0]; l != "" {
			lead[l[0]] = append(lead[l[0]], int32(i))
		} else {
			x.open = append(x.open, int32(i))
		}
	}
	for c := range x.first {
		x.first[c] = x.open
		if len(lead[c]) > 0 {
			x.first[c] = slices.Sorted(slices.Values(append(slices.Clone(x.open), lead[c]...)))
		}
	}
	return x
}

// candidates are the templates text may match, in the order they are tried: a few of the catalog's hundreds, so a
// text no template is worded as (the usual case) costs a lookup and a few string comparisons.
func (x *templateIndex) candidates(text string) []int32 {
	if text == "" {
		return x.open
	}
	return x.first[text[0]]
}

var deploymentPlaceholders = lazyre.New(`\{(\w+)\}`)
var deploymentTemplates = sync.OnceValue(func() *templateIndex { return indexTemplates(makeDeploymentTemplates()) })

func makeDeploymentTemplates() []deploymentTemplate {
	return makeCatalogTemplates(func(key string) bool {
		return strings.HasPrefix(key, "devices-deploy.") || strings.HasPrefix(key, "hub-machine.deploy_")
	})
}

func makeCatalogTemplates(eligible func(string) bool) []deploymentTemplate {
	var out []deploymentTemplate
	for _, l := range Langs {
		for key, text := range catalogs()[l] {
			if !eligible(key) {
				continue
			}
			base := key
			for _, suffix := range []string{"_zero", "_one", "_two", "_few", "_many", "_other"} {
				if strings.HasSuffix(key, suffix) {
					base = strings.TrimSuffix(key, suffix)
					break
				}
			}
			var pattern strings.Builder
			pattern.WriteString("(?s)^")
			names := []string{}
			at, fixed := 0, 0
			var literals []string
			for _, loc := range deploymentPlaceholders.FindAllStringIndex(text, -1) {
				literal := text[at:loc[0]]
				literals = append(literals, literal)
				pattern.WriteString(regexp.QuoteMeta(literal))
				fixed += len(literal)
				name := text[loc[0]+1 : loc[1]-1]
				names = append(names, name)
				if name == "count" {
					pattern.WriteString("([0-9]+)")
				} else {
					pattern.WriteString("(.+?)")
				}
				at = loc[1]
			}
			pattern.WriteString(regexp.QuoteMeta(text[at:]))
			fixed += len(text[at:])
			pattern.WriteString("$")
			literals = append(literals, text[at:])
			out = append(out, deploymentTemplate{base, &templatePattern{src: pattern.String()}, names, fixed, literals})
		}
	}
	// A more specific template (e.g. not_back_why) must precede its shorter, open-ended counterpart.
	slices.SortFunc(out, func(a, b deploymentTemplate) int {
		if a.fixed != b.fixed {
			return b.fixed - a.fixed
		}
		if n := strings.Compare(a.key, b.key); n != 0 {
			return n
		}
		return strings.Compare(a.pattern.src, b.pattern.src)
	})
	return out
}

func deploymentText(l Lang, text string, depth int) string {
	if text == "" || depth >= 8 {
		return text
	}
	forms := deploymentTemplates()
	for _, n := range forms.candidates(text) {
		form := &forms.forms[n]
		match := form.match(text)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			value := match[i+1]
			switch name {
			case "count":
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return text
				}
				args[name] = n
			case "err", "why", "wait":
				args[name] = deploymentText(l, value, depth+1)
			default:
				args[name] = value // OS, architecture and versions are data, not text to translate.
			}
		}
		return T(l, form.key, args)
	}
	return text
}
