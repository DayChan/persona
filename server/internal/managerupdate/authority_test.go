package managerupdate

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAuthorityRevisionBindsManagerMainMachineAndPersistedRow(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	base, err := authorityRecord("m-fixture", "main", "machine", "10", at, at)
	if err != nil {
		t.Fatal(err)
	}
	for _, fields := range [][4]string{{"m-other", "main", "machine", "10"}, {"m-fixture", "other", "machine", "10"},
		{"m-fixture", "main", "other", "10"}, {"m-fixture", "main", "machine", "11"}} {
		next, err := authorityRecord(fields[0], fields[1], fields[2], fields[3], at, at)
		if err != nil || next.Revision == base.Revision {
			t.Fatalf("authority conflict lost: %+v %v", fields, err)
		}
	}
	fresh, err := authorityRecord("m-fixture", "main", "machine", "10", at, at.Add(time.Second))
	if err != nil || fresh.Revision != base.Revision || !fresh.ObservedAt.After(base.ObservedAt) {
		t.Fatal("observation clock incorrectly changed the persisted revision")
	}
}

func TestAuthorityRejectsMissingOrMalformedIdentityAndRevision(t *testing.T) {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, fields := range [][4]string{{"m-fixture", "", "machine", "1"}, {"m-fixture", "main", "", "1"},
		{"m-fixture", "main", "machine", ""}, {"m-fixture", "main\nother", "machine", "1"}} {
		if _, err := authorityRecord(fields[0], fields[1], fields[2], fields[3], at, at); err == nil {
			t.Fatalf("invalid authority identity accepted: %+v", fields)
		}
	}
	if _, err := authorityRecord("m-fixture", "main", "machine", "1", time.Time{}, at); err == nil {
		t.Fatal("missing persisted revision time accepted")
	}
}

func TestAuthorityPreflightRejectsIdentityWithoutDatabaseOrWrites(t *testing.T) {
	for _, name := range []string{"uid", "verifier", "hash", "manager", "config"} {
		t.Run(name, func(t *testing.T) {
			c, p := fixture(t)
			c.FileCheck = func(string, int) error { return nil }
			switch name {
			case "uid":
				c.UID++
			case "verifier":
				c.FileCheck = nil
			case "hash":
				p.ConfigSHA256 = strings.Repeat("0", 64)
			case "manager":
				p.ManagerID = "m-foreign"
			case "config":
				path := filepath.Join(p.Dir, "manager.json")
				if err := os.WriteFile(path, []byte(`{"id":"m-test","database_url":"invalid secret DSN"}`), 0600); err != nil {
					t.Fatal(err)
				}
				var err error
				p.ConfigSHA256, err = hashFile(path)
				if err != nil {
					t.Fatal(err)
				}
			}
			before := tree(t, c.Home)
			_, err := c.ReadAuthority(context.Background(), p)
			if err == nil {
				t.Fatal("invalid authority identity accepted")
			}
			if strings.Contains(err.Error(), "secret DSN") {
				t.Fatal("database configuration leaked")
			}
			if !reflect.DeepEqual(before, tree(t, c.Home)) {
				t.Fatal("authority preflight wrote state")
			}
		})
	}
}
