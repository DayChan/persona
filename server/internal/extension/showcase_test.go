package extension

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every page of the "Component showcase" extension (schema/extension/examples/showcase, copied from its snapshots/, #1852) is a view
// this hub draws: CheckView holds, and Fit keeps all of it for an app of UI version 3 (the card in its slot, a
// widget in its family) and fits it for an app of UI version 2. The schema allows a few things the hub refuses
// (markdown images, http links); the showcase must not have them, or the apps' screenshots show views no hub sends.
func TestShowcaseIsDrawable(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "..", "schema", "extension", "examples", "showcase", "*.json"))
	if len(files) < 30 {
		t.Fatalf("%d showcase pages", len(files))
	}
	slot := func(name string) UISlot {
		s, ok := Slot(name)
		if !ok {
			t.Fatalf("no slot %s", name)
		}
		return s
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, version := range []int{2, 3, 4, 5} {
			var v View
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if err := CheckView(&v); err != nil {
				t.Errorf("%s: %v", name, err)
				break
			}
			var dropped []string
			switch {
			case strings.HasPrefix(name, "widget-"):
				if version < 3 { // only apps that draw widgets ask for them
					continue
				}
				dropped, err = FitWidget(&v, strings.TrimPrefix(name, "widget-"), version)
			case strings.HasPrefix(name, "shortcut-"): // a shortcut's result card: the large widget's subset
				dropped, err = FitWidget(&v, "large", version)
			case name == "card" || strings.HasPrefix(name, "card-"):
				dropped, err = Fit(&v, slot("overview_card"), version)
			default:
				dropped, err = Fit(&v, slot("nav_page"), version)
			}
			if err != nil {
				t.Errorf("%s for UI version %d: %v", name, version, err)
			}
			if version >= 3 && len(dropped) > 0 {
				t.Errorf("%s: the hub drops %v for an app of UI version 3", name, dropped)
			}
		}
	}
}
