package i18n

import (
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/proto"
)

// ComputerText translates only hub-owned computer/browser status templates at a response boundary.
// Canonical node/RPC errors, stored snapshots, runtime output and unknown diagnostic details stay original.
func ComputerText(l Lang, text string) string {
	if text == "" || l == "" {
		return text
	}
	forms := computerTemplates()
	for _, n := range forms.candidates(text) {
		form := &forms.forms[n]
		match := form.match(text)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			args[name] = match[i+1] // Details, names and commands are opaque data.
		}
		return T(l, form.key, args)
	}
	return text
}

var computerTemplates = sync.OnceValue(func() *templateIndex { return indexTemplates(makeComputerTemplates()) })

func makeComputerTemplates() []deploymentTemplate {
	forms := makeCatalogTemplates(func(key string) bool {
		return strings.HasPrefix(key, "hub-computer.rpc_") || strings.HasPrefix(key, "hub-browser.rpc_") ||
			key == "hub-computer.note_outdated" || key == "hub-browser.note_outdated"
	})
	for n := range forms {
		// A failed pbcopy/open/installer may emit no diagnostic output. Empty details are still recognized copy.
		forms[n].pattern.src = strings.ReplaceAll(forms[n].pattern.src, "(.+?)", "(.*?)")
	}
	return forms
}

// ComputerStatus returns a presentation copy; translating a job must never mutate the shared node snapshot.
func ComputerStatus(l Lang, c *proto.Computer) *proto.Computer {
	if c == nil {
		return nil
	}
	out := *c
	out.Note = ComputerText(l, c.Note)
	out.Job = computerJob(l, c.Job)
	return &out
}

// BrowserStatus is the browser counterpart of ComputerStatus.
func BrowserStatus(l Lang, b *proto.Browser) *proto.Browser {
	if b == nil {
		return nil
	}
	out := *b
	out.Note = ComputerText(l, b.Note)
	out.Job = computerJob(l, b.Job)
	return &out
}

func computerJob(l Lang, job *proto.ComputerJob) *proto.ComputerJob {
	if job == nil {
		return nil
	}
	out := *job
	out.Step = ComputerText(l, job.Step)
	out.Error = ComputerText(l, job.Error)
	return &out
}
