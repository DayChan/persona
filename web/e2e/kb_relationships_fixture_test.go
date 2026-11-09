package kb

// Copied into the KB package by the fixture generator. All state is disposable and synthetic.
import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestWebRelationshipFixtures(t *testing.T) {
	output := os.Getenv("WEB_KB_RELATIONSHIP_FIXTURE")
	if output == "" {
		t.Fatal("fixture generation requires an explicit output")
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	read := func(e *Engine, kind, id string) *Detail {
		t.Helper()
		d, err := e.Get(kind, id, 0)
		must(err)
		d.Path, d.Summary = "synthetic-data/"+kind+"/"+d.ID, ""
		return d
	}
	out := map[string]any{}
	func() {
		e := editEngine(t)
		must(e.Store.PutProject(&Project{Slug: "project", Name: "Example project"}))
		must(e.Store.PutItem(&Item{Kind: KindDoc, ID: "d/1", Title: "Example document", Tagging: Tagging{Pinned: "Existing facts"}}))
		_, err := e.ApplyUser(KindDoc, "d/1", UserUpdate{Update: Update{Projects: []string{"project"}}})
		must(err)
		beforeProject, beforeItem := read(e, KindProject, "project"), read(e, KindDoc, "d/1")
		project, _ := e.Store.Project("project")
		if !slices.Contains(project.Items, "docs/d_1") || beforeItem.ID != "d_1" || len(beforeProject.Links) != 1 || beforeProject.Links[0].ID != "d/1" {
			t.Fatal("unexpected raw/storage/response identity contract")
		}
		_, err = e.ApplyUser(KindDoc, "d/1", UserUpdate{Update: Update{RemoveProjects: []string{"project"}}})
		must(err)
		out["item"] = map[string]any{"beforeProject": beforeProject, "beforeItem": beforeItem, "afterProject": read(e, KindProject, "project"), "afterItem": read(e, KindDoc, "d/1")}
	}()
	for _, kind := range []string{KindChat, KindPerson} {
		e := editEngine(t)
		must(e.Store.PutProject(&Project{Slug: "project", Name: "Example project"}))
		rawID := kind + "/2"
		var update Update
		if kind == KindChat {
			must(e.Store.PutChat(&Chat{ID: rawID, Name: "Example chat"}))
			update.RemoveChats = []string{rawID}
		} else {
			must(e.Store.PutPerson(&Person{ID: rawID, Name: "Example person"}))
			update.RemovePeople = []string{rawID}
		}
		_, err := e.ApplyUser(kind, rawID, UserUpdate{Update: Update{Projects: []string{"project"}}})
		must(err)
		beforeProject, beforeEntity := read(e, KindProject, "project"), read(e, kind, rawID)
		_, err = e.ApplyUser(KindProject, "project", UserUpdate{Update: update})
		must(err)
		out[kind] = map[string]any{"beforeProject": beforeProject, "beforeEntity": beforeEntity, "afterProject": read(e, KindProject, "project"), "afterEntity": read(e, kind, rawID)}
	}

	func() {
		e := editEngine(t)
		must(e.Store.PutProject(&Project{Slug: "project", Name: "Example project"}))
		must(e.Store.PutPerson(&Person{ID: "person", Name: "Example person"}))
		_, err := e.ApplyExtension(KindProject, "project", Update{People: []string{"person"}}, ExtensionWrite{Extension: "synthetic-curator"})
		must(err)
		beforeProject, beforePerson := read(e, KindProject, "project"), read(e, KindPerson, "person")
		personDir := e.Store.Path(KindPerson, "person")
		armed := true
		e.Changed = func(change Change) {
			if armed && change.Kind == KindPerson && change.What == "metadata" {
				armed = false
				// The reverse edge has been saved; only the later attribution cleanup will fail.
				must(os.Chmod(personDir, 0500))
			}
		}
		e.ObserveChanges()
		t.Cleanup(func() { _ = os.Chmod(personDir, 0700) })
		update := UserUpdate{Update: Update{RemovePeople: []string{"person"}}}
		_, err = e.ApplyUser(KindProject, "project", update)
		must(os.Chmod(personDir, 0700))
		if err == nil {
			t.Fatal("expected a rejected write after unlink and before ownership cleanup completed")
		}
		project, _ := e.Store.Project("project")
		person, _ := e.Store.Person("person")
		if len(project.People) != 0 || len(person.Projects) != 0 || !slices.Contains(person.LockedFields, "projects") || person.Provenance["projects"].By != "ext:synthetic-curator" {
			t.Fatal("fixture must retain extension ownership despite both absent edges and protected lists")
		}
		partialProject, partialPerson := read(e, KindProject, "project"), read(e, KindPerson, "person")
		_, err = e.ApplyUser(KindProject, "project", update)
		must(err)
		person, _ = e.Store.Person("person")
		if _, owned := person.Provenance["projects"]; owned {
			t.Fatal("retry did not hand the relationship list to the user")
		}
		out["attribution"] = map[string]any{"beforeProject": beforeProject, "beforePerson": beforePerson, "partialProject": partialProject, "partialPerson": partialPerson, "afterProject": read(e, KindProject, "project"), "afterPerson": read(e, KindPerson, "person")}
	}()
	if t.Failed() {
		return
	}
	// Remove runtime timestamps from the golden data while preserving authoritative wire shapes.
	raw, err := json.Marshal(out)
	must(err)
	var stable any
	must(json.Unmarshal(raw, &stable))
	var scrub func(any)
	scrub = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			for key, child := range v {
				if text, ok := child.(string); ok {
					if _, err := time.Parse(time.RFC3339Nano, text); err == nil {
						v[key] = "2026-01-01T00:00:00Z"
					}
				}
				scrub(v[key])
			}
		case []any:
			for _, child := range v {
				scrub(child)
			}
		}
	}
	scrub(stable)
	raw, err = json.MarshalIndent(stable, "", "  ")
	must(err)
	must(os.MkdirAll(filepath.Dir(output), 0700))
	must(os.WriteFile(output, append(raw, '\n'), 0600))
}
