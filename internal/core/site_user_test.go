package core

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"weaveftpd/internal/user"
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
	for _, name := range []string{"bob", "alice", "alice.passwd", "bad name"} {
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
	for _, needle := range []string{"NUM_LOGINS", "MAX_SIM", "GROUP_SIMULT"} {
		if !strings.Contains(summary, needle) {
			t.Fatalf("siteChangeFieldSummary() missing %q in %q", needle, summary)
		}
	}
	for _, unwanted := range []string{"LOGINS", "LOGINSLOTS", "MAXSIM", "GROUPSIMULT", "SIMULT"} {
		if strings.Contains(summary, unwanted) {
			t.Fatalf("siteChangeFieldSummary() should not include alias %q in %q", unwanted, summary)
		}
	}
}

func TestHandleSiteChGrpRemovingPrimaryPromotesRemainingGroup(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteUserTestFile(t, "ek1m", "NoGroup", []string{"COCKINE", "NoGroup"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100, "COCKINE": 101},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteChGrp([]string{"ek1m", "NoGroup"})

	reply := conn.String()
	if !strings.Contains(reply, "200 ek1m: removed NoGroup primary NoGroup->COCKINE.") {
		t.Fatalf("unexpected CHGRP reply %q", reply)
	}
	updated, err := user.LoadUser("ek1m", s.GroupMap)
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	if updated.PrimaryGroup != "COCKINE" {
		t.Fatalf("PrimaryGroup = %q, want COCKINE", updated.PrimaryGroup)
	}
	if _, ok := updated.Groups["NoGroup"]; ok {
		t.Fatalf("NoGroup still present in groups: %#v", updated.Groups)
	}
	if _, ok := updated.Groups["COCKINE"]; !ok {
		t.Fatalf("COCKINE missing from groups: %#v", updated.Groups)
	}
	if updated.GID != 101 {
		t.Fatalf("GID = %d, want 101", updated.GID)
	}
}

func TestHandleSiteChGrpRejectsRemovingOnlyPrimaryGroup(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteUserTestFile(t, "ek1m", "NoGroup", []string{"NoGroup"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteChGrp([]string{"ek1m", "NoGroup"})

	reply := conn.String()
	if !strings.Contains(reply, "550 Cannot remove NoGroup from ek1m because it is the primary group and no other group remains.") {
		t.Fatalf("unexpected CHGRP reply %q", reply)
	}
	updated, err := user.LoadUser("ek1m", s.GroupMap)
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	if updated.PrimaryGroup != "NoGroup" {
		t.Fatalf("PrimaryGroup = %q, want NoGroup", updated.PrimaryGroup)
	}
	if _, ok := updated.Groups["NoGroup"]; !ok {
		t.Fatalf("NoGroup missing from groups after rejected change: %#v", updated.Groups)
	}
}

func TestCreateUserFromArgsWithPrimaryDropsDefaultNoGroup(t *testing.T) {
	withSiteUserTestDir(t)
	s := &Session{
		GroupMap: map[string]int{"NoGroup": 100, "COCKINE": 101},
	}

	u, _, err := createUserFromArgs(s, "ek1m", "secret", "COCKINE", nil)
	if err != nil {
		t.Fatalf("createUserFromArgs() error = %v", err)
	}
	if u.PrimaryGroup != "COCKINE" {
		t.Fatalf("PrimaryGroup = %q, want COCKINE", u.PrimaryGroup)
	}
	if _, ok := u.Groups["NoGroup"]; ok {
		t.Fatalf("NoGroup survived as secondary group: %#v", u.Groups)
	}
	if _, ok := u.Groups["COCKINE"]; !ok {
		t.Fatalf("COCKINE missing from groups: %#v", u.Groups)
	}
}

func TestHandleSiteChPGrpDropsDefaultNoGroupSecondary(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteUserTestFile(t, "ek1m", "NoGroup", []string{"NoGroup"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100, "COCKINE": 101},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteChPGrp([]string{"ek1m", "COCKINE"})

	if !strings.Contains(conn.String(), "200 Primary group changed.") {
		t.Fatalf("unexpected CHPGRP reply %q", conn.String())
	}
	updated, err := user.LoadUser("ek1m", s.GroupMap)
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	if updated.PrimaryGroup != "COCKINE" {
		t.Fatalf("PrimaryGroup = %q, want COCKINE", updated.PrimaryGroup)
	}
	if _, ok := updated.Groups["NoGroup"]; ok {
		t.Fatalf("NoGroup survived as secondary group: %#v", updated.Groups)
	}
	if _, ok := updated.Groups["COCKINE"]; !ok {
		t.Fatalf("COCKINE missing from groups: %#v", updated.Groups)
	}
}

func TestHandleSiteGrpAddRejectsExistingGroup(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteGroupTestFiles(t, map[string]int{"COCKINE": 101})
	existingPath := filepath.Join("etc", "groups", "COCKINE")
	before, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("ReadFile(existing group) error = %v", err)
	}

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"COCKINE": 101},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteGrpAdd([]string{"COCKINE", "new description"})

	if !strings.Contains(conn.String(), "550 Group COCKINE already exists.") {
		t.Fatalf("unexpected GRPADD reply %q", conn.String())
	}
	after, err := os.ReadFile(existingPath)
	if err != nil {
		t.Fatalf("ReadFile(existing group after GRPADD) error = %v", err)
	}
	if string(after) != string(before) {
		t.Fatalf("GRPADD overwrote existing group file\nbefore=%q\nafter=%q", string(before), string(after))
	}
}

func TestHandleSiteGrpAddRejectsInvalidGroupName(t *testing.T) {
	withSiteUserTestDir(t)

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteGrpAdd([]string{"../evil"})

	if !strings.Contains(conn.String(), `550 Invalid group name "../evil".`) {
		t.Fatalf("unexpected GRPADD reply %q", conn.String())
	}
	if _, err := os.Stat(filepath.Join("etc", "evil")); !os.IsNotExist(err) {
		t.Fatalf("invalid GRPADD should not write outside groups dir, stat err=%v", err)
	}
}

func TestHandleSiteGrpDelRejectsGroupWithMembers(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteGroupTestFiles(t, map[string]int{"NoGroup": 100, "COCKINE": 101})
	writeSiteUserTestFile(t, "ek1m", "COCKINE", []string{"COCKINE"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100, "COCKINE": 101},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteGrpDel([]string{"COCKINE"})

	if !strings.Contains(conn.String(), "550 Group COCKINE still has 1 member(s): ek1m.") {
		t.Fatalf("unexpected GRPDEL reply %q", conn.String())
	}
	if _, err := os.Stat(filepath.Join("etc", "groups", "COCKINE")); err != nil {
		t.Fatalf("group config should remain after rejected GRPDEL: %v", err)
	}
	groupFile, err := os.ReadFile(filepath.Join("etc", "group"))
	if err != nil {
		t.Fatalf("ReadFile(etc/group) error = %v", err)
	}
	if !strings.Contains(string(groupFile), "COCKINE:COCKINE:101:") {
		t.Fatalf("etc/group lost COCKINE after rejected GRPDEL:\n%s", string(groupFile))
	}
}

func TestHandleSiteGrpNfoRejectsInvalidGroupName(t *testing.T) {
	withSiteUserTestDir(t)

	conn := &bufferConn{}
	s := &Session{
		Conn:   conn,
		Config: &Config{},
		User:   &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteGrpNfo([]string{"../group"})

	if !strings.Contains(conn.String(), `550 Invalid group name "../group".`) {
		t.Fatalf("unexpected GRPNFO reply %q", conn.String())
	}
}

func TestHandleSiteAddUserRollsBackUserFileWhenPasswdUpdateFails(t *testing.T) {
	withSiteUserTestDir(t)

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{PasswdFile: filepath.Join("missing", "passwd")},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteAddUser([]string{"ek1m", "secret"})

	if !strings.Contains(conn.String(), "550 Failed to update passwd for ek1m:") {
		t.Fatalf("unexpected ADDUSER reply %q", conn.String())
	}
	if _, err := os.Stat(filepath.Join("etc", "users", "ek1m")); !os.IsNotExist(err) {
		t.Fatalf("user file should be rolled back after passwd failure, stat err=%v", err)
	}
}

func TestHandleSiteReAddRequiresPasswordBeforeMovingDeletedUser(t *testing.T) {
	withSiteUserTestDir(t)
	if err := os.MkdirAll(deletedUsersDir, 0755); err != nil {
		t.Fatalf("MkdirAll(deleted) error = %v", err)
	}
	writeSiteDeletedUserTestFile(t, "ek1m", "NoGroup", []string{"NoGroup"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{PasswdFile: filepath.Join("etc", "passwd")},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteReAdd([]string{"ek1m"})

	if !strings.Contains(conn.String(), "550 No stored password available.") {
		t.Fatalf("unexpected READD reply %q", conn.String())
	}
	if _, err := os.Stat(deletedUserPath("ek1m")); err != nil {
		t.Fatalf("deleted user should remain in deleted store, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join("etc", "users", "ek1m")); !os.IsNotExist(err) {
		t.Fatalf("active user should not be restored without password, stat err=%v", err)
	}
}

func TestHandleSiteRenUserRollsBackNewUserWhenPasswdRenameFails(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteUserTestFile(t, "olduser", "NoGroup", []string{"NoGroup"})
	if err := os.WriteFile(filepath.Join("etc", "passwd"), []byte("someone:hash:1000:100:/site:/bin/false\n"), 0600); err != nil {
		t.Fatalf("WriteFile(passwd) error = %v", err)
	}

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{PasswdFile: filepath.Join("etc", "passwd")},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteRenUser([]string{"olduser", "newuser"})

	if !strings.Contains(conn.String(), "550 Failed to rename passwd entry:") {
		t.Fatalf("unexpected RENUSER reply %q", conn.String())
	}
	if _, err := os.Stat(filepath.Join("etc", "users", "olduser")); err != nil {
		t.Fatalf("old user should remain after failed rename, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join("etc", "users", "newuser")); !os.IsNotExist(err) {
		t.Fatalf("new user file should be rolled back, stat err=%v", err)
	}
}

func TestHandleSiteAddIPRejectsInvalidMask(t *testing.T) {
	withSiteUserTestDir(t)
	writeSiteUserTestFile(t, "ek1m", "NoGroup", []string{"NoGroup"})

	conn := &bufferConn{}
	s := &Session{
		Conn:     conn,
		Config:   &Config{},
		GroupMap: map[string]int{"NoGroup": 100},
		User:     &user.User{Name: "weaveftpd", Flags: "1"},
	}

	s.HandleSiteAddIP([]string{"ek1m", "not-a-host"})

	if !strings.Contains(conn.String(), `550 Invalid IP mask "not-a-host".`) {
		t.Fatalf("unexpected ADDIP reply %q", conn.String())
	}
	updated, err := user.LoadUser("ek1m", s.GroupMap)
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	if len(updated.IPs) != 0 {
		t.Fatalf("invalid ADDIP changed IPs: %#v", updated.IPs)
	}
}

func withSiteUserTestDir(t *testing.T) {
	t.Helper()
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join("etc", "users"), 0755); err != nil {
		t.Fatalf("MkdirAll(users) error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join("etc", "groups"), 0755); err != nil {
		t.Fatalf("MkdirAll(groups) error = %v", err)
	}
}

func writeSiteUserTestFile(t *testing.T, name, primary string, groups []string) {
	t.Helper()
	writeSiteUserTestFileAt(t, filepath.Join("etc", "users", name), primary, groups)
}

func writeSiteDeletedUserTestFile(t *testing.T, name, primary string, groups []string) {
	t.Helper()
	writeSiteUserTestFileAt(t, deletedUserPath(name), primary, groups)
}

func writeSiteUserTestFileAt(t *testing.T, filePath, primary string, groups []string) {
	t.Helper()
	lines := []string{
		"USER Added by weaveftpd",
		"GENERAL 0,120 -1 0 0",
		"LOGINS 16 0 6 10",
		"TIMEFRAME 0 0",
		"FLAGS 3",
		"TAGLINE No Tagline Set",
		"HOMEDIR /site",
		"DIR /",
		"ADDED 1712306777 weaveftpd",
		"EXPIRES 0",
		"CREDITS 0 0",
		"RATIO 0 0",
		"ALLUP 0 0 0",
		"ALLDN 0 0 0",
		"WKUP 0 0 0",
		"WKDN 0 0 0",
		"DAYUP 0 0 0",
		"DAYDN 0 0 0",
		"MONTHUP 0 0 0",
		"MONTHDN 0 0 0",
		"NUKE 0 0 0",
		"TIME 0 0 0 0 1712306777",
		"PRIMARY_GROUP " + primary,
	}
	for _, group := range groups {
		lines = append(lines, "GROUP "+group+" 0")
	}
	if err := os.WriteFile(filePath, []byte(strings.Join(lines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}
}

func writeSiteGroupTestFiles(t *testing.T, groups map[string]int) {
	t.Helper()
	names := make([]string, 0, len(groups))
	for group := range groups {
		names = append(names, group)
	}
	sort.Strings(names)

	var groupLines []string
	for _, group := range names {
		gid := groups[group]
		groupLines = append(groupLines, group+":"+group+":"+strconv.Itoa(gid)+":")
		if err := os.WriteFile(filepath.Join("etc", "groups", group), []byte("GROUP "+group+"\n"), 0600); err != nil {
			t.Fatalf("WriteFile(group %s) error = %v", group, err)
		}
	}
	if err := os.WriteFile(filepath.Join("etc", "group"), []byte(strings.Join(groupLines, "\n")+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile(etc/group) error = %v", err)
	}
}
