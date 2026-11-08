package hub

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/i18n"
)

type compatibleHubUnits struct {
	service, socket string
	legacy          bool
}

func (e *setupEnv) compatibleHubBinary() (string, error) {
	current := filepath.Join(e.root, "bin", "personad")
	old := filepath.Join(e.root, "bin", compat.DaemonBinary)
	a, ae := os.Stat(current)
	b, be := os.Stat(old)
	if ae != nil && !os.IsNotExist(ae) {
		return "", ae
	}
	if be != nil && !os.IsNotExist(be) {
		return "", be
	}
	if ae == nil && a.IsDir() || be == nil && b.IsDir() || ae == nil && be == nil && !os.SameFile(a, b) {
		return "", i18n.Err("hub-compatibility.service_conflict")
	}
	primary, alias := current, old
	if be == nil {
		primary, alias = old, current
	}
	if ae != nil && be != nil {
		exe, err := e.exe()
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(primary), 0o755); err != nil {
			return "", err
		}
		if err := copyFile(exe, primary, 0o755); err != nil {
			return "", err
		}
	}
	if _, err := os.Lstat(alias); os.IsNotExist(err) {
		// The former name is made only for an installation whose units still run it (#1445); one on personad's own
		// units gets none. A binary kept under the former name still gets personad beside it.
		if alias == old {
			if units, err := e.compatibleHubUnits(); err != nil || !units.legacy {
				return filepath.EvalSymlinks(primary)
			}
		}
		if err := os.Symlink(filepath.Base(primary), alias); err != nil && !os.IsExist(err) {
			return "", err
		}
	} else if err != nil {
		return "", err
	}
	a, ae = os.Stat(primary)
	b, be = os.Stat(alias)
	if ae != nil || be != nil || !os.SameFile(a, b) {
		return "", i18n.Err("hub-compatibility.service_conflict")
	}
	// Update the real file, not a symlink: an atomic swap must retain both names.
	return filepath.EvalSymlinks(primary)
}

func (e *setupEnv) compatibleHubUnits() (compatibleHubUnits, error) {
	current := compatibleHubUnits{"personad", "personad.socket", false}
	old := compatibleHubUnits{compat.HubService, compat.HubSocket, true}
	hasCurrent, hasOld := false, false
	for _, kind := range []string{"service", "socket"} {
		a, aerr := os.Stat(filepath.Join(e.unitDir(), current.service+"."+kind))
		b, berr := os.Stat(filepath.Join(e.unitDir(), old.service+"."+kind))
		hasCurrent = hasCurrent || aerr == nil
		hasOld = hasOld || berr == nil
		if aerr == nil && berr == nil && !os.SameFile(a, b) {
			return current, i18n.Err("hub-compatibility.service_conflict")
		}
	}
	if hasOld && hasCurrent {
		// A complete pair of exact file aliases is one installation, not two.
		for _, kind := range []string{"service", "socket"} {
			a, ae := os.Stat(filepath.Join(e.unitDir(), current.service+"."+kind))
			b, be := os.Stat(filepath.Join(e.unitDir(), old.service+"."+kind))
			if (ae == nil) != (be == nil) || ae == nil && !os.SameFile(a, b) {
				return current, i18n.Err("hub-compatibility.service_conflict")
			}
		}
	}
	if hasOld && !hasCurrent {
		return old, nil
	}
	return current, nil
}
func (e *setupEnv) compatibleHubName(name string) string {
	units, err := e.compatibleHubUnits()
	if err != nil || !units.legacy {
		return name
	}
	switch name {
	case "personad":
		return units.service
	case "personad.socket":
		return units.socket
	}
	return name
}
func compatibleUnitText(text string, units compatibleHubUnits) string {
	if !units.legacy {
		return text
	}
	return strings.NewReplacer("personad.service", units.service+".service", "personad.socket", units.socket).Replace(text)
}
func sameSocketAddresses(text string, want []string) bool {
	var have []string
	for _, line := range strings.Split(text, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "ListenStream="); ok {
			have = append(have, value)
		}
	}
	if len(have) != len(want) {
		return false
	}
	for i, value := range want {
		if strings.HasPrefix(value, ":") {
			value = value[1:]
		}
		if value != have[i] {
			return false
		}
	}
	return true
}
