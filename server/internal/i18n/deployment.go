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
	pattern *regexp.Regexp
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

// match is the pattern's submatches in text, nil when it does not match. A text is tried against a catalog's hundreds
// of templates in turn, and nearly all of them do not fit it: those cost a few string comparisons, not a regexp run.
func (t *deploymentTemplate) match(text string) []string {
	if !t.fits(text) {
		return nil
	}
	return t.pattern.FindStringSubmatch(text)
}

var deploymentPlaceholders = lazyre.New(`\{(\w+)\}`)
var deploymentTemplates = sync.OnceValue(makeDeploymentTemplates)

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
			out = append(out, deploymentTemplate{base, regexp.MustCompile(pattern.String()), names, fixed, literals})
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
		return strings.Compare(a.pattern.String(), b.pattern.String())
	})
	return out
}

func deploymentText(l Lang, text string, depth int) string {
	if text == "" || depth >= 8 {
		return text
	}
	forms := deploymentTemplates()
	for i := range forms { // by index: a range value copies each template, for every text
		form := &forms[i]
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
