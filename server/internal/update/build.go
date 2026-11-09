// Package update is how a hub knows what it is and whether a newer one is out (docs/operations/hub-update.md): the build it was
// made as (channel, build number), the manifest the publisher puts next to the downloads (latest.json, with every
// file's sha256), the publisher's checks before a release, and installing one (download, swap, the watchdog).
package update

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Channels: how a binary was built, which decides whether it updates itself.
const (
	Release = "release" // published by publish.sh, or deployed straight from master (deploy.sh): updates itself
	Docker  = "docker"  // in the Docker image: says when a newer image is out, the user pulls it
	Dev     = "dev"     // a local build: shows its version only
)

// Build is what this binary is.
type Build struct {
	Version   string // for people: git describe and the time
	Channel   string // Release, Docker or Dev
	Number    int64  // the build number: when it was built, UTC YYYYMMDDHHMM; 0 when unknown
	UpdateURL string // where the publisher's dl/ is (latest.json); empty: nowhere to look
	Image     string // the Docker image it was built as (ghcr.io/<owner>/<repo>): where a Docker hub looks for newer ones
	// Commit is the commit it was built from (full hex id), with "-dirty" after it when the tree had changes not
	// committed; empty when the build did not say (Hold).
	Commit string
}

// Make turns what the build wrote into the binary (-X main.version, main.channel, main.build, main.updateURL) into a
// Build. An unknown channel is Dev; a release without a valid build number is Dev too (it could not tell newer from
// older, so it must not update).
func Make(version, channel, number, updateURL string) Build {
	b := Build{Version: version, Channel: Dev, UpdateURL: updateURL}
	n, err := ParseNumber(number)
	if err == nil {
		b.Number = n
	}
	switch channel {
	case Release:
		if err == nil {
			b.Channel = Release
		}
	case Docker:
		b.Channel = Docker
	}
	return b
}

// SelfUpdates says whether this binary replaces itself with newer releases.
func (b Build) SelfUpdates() bool { return b.Channel == Release && b.Number > 0 && b.UpdateURL != "" }

// ParseNumber reads a build number: twelve digits, a UTC time YYYYMMDDHHMM.
func ParseNumber(s string) (int64, error) {
	if len(s) != 12 {
		return 0, fmt.Errorf("build number %q is not YYYYMMDDHHMM", s)
	}
	if _, err := time.Parse("200601021504", s); err != nil {
		return 0, fmt.Errorf("build number %q is not a time: %w", s, err)
	}
	return strconv.ParseInt(s, 10, 64)
}

// Newer says whether a release numbered latest replaces this build: only a strictly larger number does. A build
// deployed straight from master carries its build time, so an older release seen later never downgrades it
// (TestNoDowngrade).
func (b Build) Newer(latest int64) bool { return b.Number > 0 && latest > b.Number }

// Hold says why this build must not install release m by itself, or "" when it may: m's code has to include the
// commit this build was made from. A release published from older code (a checkout behind the master a hub was
// deployed from) has the larger build number all the same, and would take that hub back (docs/operations/hub-update.md
// "No rollback"). The reason is for people: the app shows it, the update history keeps it.
func (b Build) Hold(m *Manifest) string {
	commit, dirty := strings.CutSuffix(b.Commit, "-dirty")
	if commit == "" {
		return "这个 hub 没有记下自己是从哪个提交编出来的，确认不了新版本包含它的代码"
	}
	if dirty {
		return "这个 hub 是用带着没提交的改动的代码编的，新版本不一定包含这些改动"
	}
	if _, err := commitKey(commit); err != nil {
		return fmt.Sprintf("这个 hub 记下的提交 %q 不完整，确认不了新版本包含它的代码", commit)
	}
	if strings.EqualFold(m.Commit, commit) {
		return ""
	}
	ok, err := m.Contains(commit)
	switch {
	case errors.Is(err, errNoAncestors):
		return "新版本的清单里没有提交记录（发布它的 publish.sh 太旧），确认不了它包含这个 hub 的代码"
	case err != nil:
		return "新版本的清单里的提交记录读不出来"
	case !ok:
		return fmt.Sprintf("新版本（提交 %.8s）不包含这个 hub 正在运行的提交 %.8s：发布用的代码比这里的旧，或在另一条分支上，自动装了会退回", m.Commit, commit)
	}
	return ""
}
