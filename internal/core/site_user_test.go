package core

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestListDeletedUsersFiltersPasswdFiles(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	if err := os.MkdirAll(deletedUsersDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"bob", "alice", "alice.passwd"} {
		if err := os.WriteFile(filepath.Join(deletedUsersDir, name), []byte("x"), 0600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}

	got, err := listDeletedUsers()
	if err != nil {
		t.Fatalf("listDeletedUsers() error = %v", err)
	}
	want := []string{"alice", "bob"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("listDeletedUsers() = %#v, want %#v", got, want)
	}
}

func TestSaveUserIPsOnlyPreservesUserfileGroups(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	defer func() { _ = os.Chdir(oldWD) }()
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join("etc", "users"), 0755); err != nil {
		t.Fatalf("MkdirAll(users) error = %v", err)
	}

	userPath := filepath.Join("etc", "users", "Finity")
	input := strings.Join([]string{
		"USER Imported account",
		"GENERAL 0,120 -1 0 0",
		"LOGINS 16 0 6 10",
		"FLAGS 3",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
		"GROUP Friends 1",
		"CUSTOM keep-this-line",
		"IP *@1.1.1.1",
		"IP ident@2.2.2.*",
	}, "\n") + "\n"
	if err := os.WriteFile(userPath, []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	if err := saveUserIPsOnly("Finity", []string{"*@3.3.3.3"}); err != nil {
		t.Fatalf("saveUserIPsOnly() error = %v", err)
	}

	out, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile(user) error = %v", err)
	}
	text := string(out)
	for _, needle := range []string{
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
		"GROUP Friends 1",
		"CUSTOM keep-this-line",
		"IP *@3.3.3.3",
	} {
		if !strings.Contains(text, needle) {
			t.Fatalf("saved userfile missing %q\n%s", needle, text)
		}
	}
	for _, oldIP := range []string{"IP *@1.1.1.1", "IP ident@2.2.2.*"} {
		if strings.Contains(text, oldIP) {
			t.Fatalf("saved userfile still contains old IP %q\n%s", oldIP, text)
		}
	}
}

func TestSiteChangeFieldSummaryUsesCanonicalNames(t *testing.T) {
	summary := siteChangeFieldSummary()
	fields := map[string]bool{}
	for _, field := range strings.Split(summary, ",") {
		fields[strings.TrimSpace(field)] = true
	}
	for _, needle := range []string{"NUM_LOGINS", "MAX_SIM", "GROUP_SIMULT"} {
		if !fields[needle] {
			t.Fatalf("siteChangeFieldSummary() missing %q in %q", needle, summary)
		}
	}
	for _, unwanted := range []string{"LOGINS", "LOGINSLOTS", "MAXSIM", "GROUPSIMULT", "SIMULT"} {
		if fields[unwanted] {
			t.Fatalf("siteChangeFieldSummary() should not include alias %q in %q", unwanted, summary)
		}
	}
}
