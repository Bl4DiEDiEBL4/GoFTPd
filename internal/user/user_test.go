package user

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadAndSavePreservesImportedUserfileFields(t *testing.T) {
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
	passwd := "Finity:$01020304$d631702386055e6797948aa58b4551b2ba70492a:100:300:0:/site:/bin/false\n"
	if err := os.WriteFile(filepath.Join("etc", "passwd"), []byte(passwd), 0600); err != nil {
		t.Fatalf("WriteFile(passwd) error = %v", err)
	}

	input := strings.Join([]string{
		"USER Added by glftpd",
		"GENERAL 0,120 -1 0 0",
		"LOGINS 16 0 6 10",
		"TIMEFRAME 0 0",
		"FLAGS 3",
		"TAGLINE No Tagline Set",
		"HOMEDIR /site",
		"DIR /",
		"ADDED 1712306777 glftpd",
		"EXPIRES 0",
		"CREDITS 245752440015 0",
		"RATIO 3 -1",
		"LOGINSLOTS 16",
		"MAXSIM 0",
		"UPLOADSLOTS 0",
		"DOWNLOADSLOTS 0",
		"WKLYALLOTMENT 0",
		"GROUPSLOTS 0 0",
		"ALLUP 962989 106564602869 283909879 0 0 0",
		"ALLDN 616476 61577907703 248884788 0 0 0",
		"WKUP 224 28231629 1588 0 0 0",
		"WKDN 1356 155321900 44062 0 0 0",
		"DAYUP 160 18238879 1217 0 0 0",
		"DAYDN 906 98911324 29735 0 0 0",
		"MONTHUP 1347 128797999 105168 0 0 0",
		"MONTHDN 5371 547778033 925506 0 0 0",
		"NUKE 1777936801 1069 2480594 0 0 0",
		"TIME 1602990 1778010002 0 235935",
		"GROUP Friends 0",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
		"IP *@5.186.48.*",
	}, "\n") + "\n"
	userPath := filepath.Join("etc", "users", "Finity")
	if err := os.WriteFile(userPath, []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	u, err := LoadUser("Finity", map[string]int{"iND": 300, "Friends": 301})
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	if err := u.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join("etc", "users", ".backup", "Finity")); !os.IsNotExist(err) {
		t.Fatalf("legacy per-write backup exists or stat failed: %v", err)
	}
	if u.UploadSlots != 10 {
		t.Fatalf("UploadSlots = %d, want 10 derived from LOGINS", u.UploadSlots)
	}
	if u.DownloadSlots != 6 {
		t.Fatalf("DownloadSlots = %d, want 6 derived from LOGINS", u.DownloadSlots)
	}
	if u.LoginSlots != 16 {
		t.Fatalf("LoginSlots = %d, want 16 derived from LOGINS", u.LoginSlots)
	}
	if u.MaxSim != 0 {
		t.Fatalf("MaxSim = %d, want 0", u.MaxSim)
	}

	out, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile(saved user) error = %v", err)
	}
	text := string(out)
	checks := []string{
		"USER Added by glftpd",
		"GENERAL 0,120 -1 0 0",
		"LOGINS 16 0 6 10",
		"TIMEFRAME 0 0",
		"ADDED 1712306777 glftpd",
		"CREDITS 245752440015 0",
		"RATIO 3 -1",
		"LOGINSLOTS 16",
		"MAXSIM 0",
		"UPLOADSLOTS 10",
		"DOWNLOADSLOTS 6",
		"WKLYALLOTMENT 0",
		"GROUPSLOTS 0 0",
		"ALLUP 962989 106564602869 283909879 0 0 0",
		"NUKE 1777936801 1069 2480594 0 0 0",
		"TIME 1602990 1778010002 0 235935",
		"PRIMARY_GROUP iND",
		"GROUP Friends 0",
		"GROUP iND 0",
	}
	for _, needle := range checks {
		if !strings.Contains(text, needle) {
			t.Fatalf("saved userfile missing %q\n%s", needle, text)
		}
	}
}

func TestLoadUserCacheReturnsIndependentCopies(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join("etc", "passwd"), []byte("Finity:x:100:300:0:/site:/bin/false\n"), 0600); err != nil {
		t.Fatalf("WriteFile(passwd) error = %v", err)
	}
	input := strings.Join([]string{
		"FLAGS 3",
		"HOMEDIR /site",
		"DIR /",
		"GROUP iND 0",
		"IP *@127.0.0.1",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join("etc", "users", "Finity"), []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	first, err := LoadUser("Finity", map[string]int{"iND": 300})
	if err != nil {
		t.Fatalf("LoadUser(first) error = %v", err)
	}
	second, err := LoadUser("Finity", map[string]int{"iND": 300})
	if err != nil {
		t.Fatalf("LoadUser(second) error = %v", err)
	}

	first.Groups["LEAK"] = 1
	first.IPs[0] = "*@10.0.0.1"
	first.StatExtras["ALLUP"] = "mutated"

	if _, ok := second.Groups["LEAK"]; ok {
		t.Fatal("second cached user shared Groups map with first")
	}
	if got := second.IPs[0]; got != "*@127.0.0.1" {
		t.Fatalf("second cached user shared IP slice with first: %q", got)
	}
	if got := second.StatExtras["ALLUP"]; got != "" {
		t.Fatalf("second cached user shared StatExtras map with first: %q", got)
	}
}

func TestSaveRefusesToStripAccountFields(t *testing.T) {
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
		"CREDITS 245752440015 0",
		"RATIO 3 -1",
		"ALLUP 10 1000 0",
		"ALLDN 0 0 0",
		"WKUP 10 1000 0",
		"WKDN 0 0 0",
		"DAYUP 10 1000 0",
		"DAYDN 0 0 0",
		"MONTHUP 10 1000 0",
		"MONTHDN 0 0 0",
		"NUKE 0 0 0",
		"TIME 0 0 0 0 1712306777",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
		"IP *@1.2.3.4",
	}, "\n") + "\n"
	if err := os.WriteFile(userPath, []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	u := &User{
		Name:       "Finity",
		Groups:     map[string]int{},
		Ratio:      0,
		Credits:    0,
		AllUp:      StatLine{Files: 11, Bytes: 1100},
		WkUp:       StatLine{Files: 11, Bytes: 1100},
		DayUp:      StatLine{Files: 11, Bytes: 1100},
		MonthUp:    StatLine{Files: 11, Bytes: 1100},
		StatExtras: map[string]string{},
	}
	if err := u.Save(); err == nil {
		t.Fatalf("Save() succeeded while stripping account fields")
	}
	out, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile(user) error = %v", err)
	}
	if string(out) != input {
		t.Fatalf("unsafe save changed the original file\n%s", string(out))
	}
}

func TestSaveRefusesToReduceAllTimeStats(t *testing.T) {
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
		"CREDITS 245752440015 0",
		"RATIO 3 -1",
		"ALLUP 236 24605139109 0",
		"ALLDN 10 1000 0",
		"WKUP 236 24605139109 0",
		"WKDN 10 1000 0",
		"DAYUP 236 24605139109 0",
		"DAYDN 10 1000 0",
		"MONTHUP 236 24605139109 0",
		"MONTHDN 10 1000 0",
		"NUKE 0 0 0",
		"TIME 0 0 0 0 1712306777",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
		"IP *@1.2.3.4",
	}, "\n") + "\n"
	if err := os.WriteFile(userPath, []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	u, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser() error = %v", err)
	}
	u.AllUp = StatLine{Files: 1, Bytes: 1}
	if err := u.Save(); err == nil {
		t.Fatalf("Save() succeeded while reducing ALLUP")
	}
	out, err := os.ReadFile(userPath)
	if err != nil {
		t.Fatalf("ReadFile(user) error = %v", err)
	}
	if string(out) != input {
		t.Fatalf("unsafe stat save changed the original file\n%s", string(out))
	}
}

func TestLoadUserRejectsEmptyUserfile(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join("etc", "users", "Finity"), nil, 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}
	if _, err := LoadUser("Finity", nil); err == nil {
		t.Fatalf("LoadUser() succeeded for empty userfile")
	}
}

func TestResetTransferStatPeriodsIfDueUsesDedicatedPeriodAnchor(t *testing.T) {
	u := &User{
		LastLogin:    time.Date(2026, 5, 13, 20, 0, 0, 0, time.Local).Unix(),
		PeriodAnchor: time.Date(2026, 5, 13, 23, 55, 0, 0, time.Local).Unix(),
		DayUp:        StatLine{Files: 10, Bytes: 1000},
		WkUp:         StatLine{Files: 20, Bytes: 2000},
		MonthUp:      StatLine{Files: 30, Bytes: 3000},
	}

	now := time.Date(2026, 5, 14, 0, 5, 0, 0, time.Local)
	if !u.ResetTransferStatPeriodsIfDue(now) {
		t.Fatalf("expected rollover to reset stats")
	}
	if u.DayUp.Files != 0 || u.DayUp.Bytes != 0 {
		t.Fatalf("day stats were not reset: %+v", u.DayUp)
	}
	if u.PeriodAnchor != now.Unix() {
		t.Fatalf("PeriodAnchor = %d, want %d", u.PeriodAnchor, now.Unix())
	}
	if u.LastLogin != time.Date(2026, 5, 13, 20, 0, 0, 0, time.Local).Unix() {
		t.Fatalf("LastLogin should remain unchanged")
	}
}

func TestUpdateStatsWithCreditsMergesParallelSessions(t *testing.T) {
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
	passwd := "Finity:x:100:300:0:/site:/bin/false\n"
	if err := os.WriteFile(filepath.Join("etc", "passwd"), []byte(passwd), 0600); err != nil {
		t.Fatalf("WriteFile(passwd) error = %v", err)
	}
	input := strings.Join([]string{
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
		"CREDITS 0",
		"RATIO 3",
		"LOGINSLOTS 16",
		"MAXSIM 0",
		"UPLOADSLOTS 10",
		"DOWNLOADSLOTS 6",
		"WKLYALLOTMENT 0",
		"GROUPSLOTS 0 0",
		"ALLUP 0 0 0",
		"ALLDN 0 0 0",
		"WKUP 0 0 0",
		"WKDN 0 0 0",
		"DAYUP 0 0 0",
		"DAYDN 0 0 0",
		"MONTHUP 0 0 0",
		"MONTHDN 0 0 0",
		"NUKE 0 0 0",
		"TIME 0 0 0 0 0",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join("etc", "users", "Finity"), []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	u1, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(u1) error = %v", err)
	}
	u2, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(u2) error = %v", err)
	}
	u1.UpdateStatsWithCredits(100, true, true)
	u2.UpdateStatsWithCredits(200, true, true)

	saved, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(saved) error = %v", err)
	}
	if saved.DayUp.Files != 2 || saved.DayUp.Bytes != 300 {
		t.Fatalf("saved DayUp = %+v, want 2 files / 300 bytes", saved.DayUp)
	}
	if saved.AllUp.Files != 2 || saved.AllUp.Bytes != 300 {
		t.Fatalf("saved AllUp = %+v, want 2 files / 300 bytes", saved.AllUp)
	}
	if saved.WkUp.Files != 2 || saved.WkUp.Bytes != 300 {
		t.Fatalf("saved WkUp = %+v, want 2 files / 300 bytes", saved.WkUp)
	}
	if saved.MonthUp.Files != 2 || saved.MonthUp.Bytes != 300 {
		t.Fatalf("saved MonthUp = %+v, want 2 files / 300 bytes", saved.MonthUp)
	}
	if saved.Credits != 900 {
		t.Fatalf("saved Credits = %d, want 900", saved.Credits)
	}
}

func TestUpdateDownloadStatsWithCreditsMergesParallelSessions(t *testing.T) {
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
	passwd := "Finity:x:100:300:0:/site:/bin/false\n"
	if err := os.WriteFile(filepath.Join("etc", "passwd"), []byte(passwd), 0600); err != nil {
		t.Fatalf("WriteFile(passwd) error = %v", err)
	}
	input := strings.Join([]string{
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
		"CREDITS 1000",
		"RATIO 3",
		"LOGINSLOTS 16",
		"MAXSIM 0",
		"UPLOADSLOTS 10",
		"DOWNLOADSLOTS 6",
		"WKLYALLOTMENT 0",
		"GROUPSLOTS 0 0",
		"ALLUP 0 0 0",
		"ALLDN 0 0 0",
		"WKUP 0 0 0",
		"WKDN 0 0 0",
		"DAYUP 0 0 0",
		"DAYDN 0 0 0",
		"MONTHUP 0 0 0",
		"MONTHDN 0 0 0",
		"NUKE 0 0 0",
		"TIME 0 0 0 0 0",
		"PRIMARY_GROUP iND",
		"GROUP iND 0",
	}, "\n") + "\n"
	if err := os.WriteFile(filepath.Join("etc", "users", "Finity"), []byte(input), 0600); err != nil {
		t.Fatalf("WriteFile(user) error = %v", err)
	}

	u1, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(u1) error = %v", err)
	}
	u2, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(u2) error = %v", err)
	}
	u1.UpdateStatsWithCredits(100, false, true)
	u2.UpdateStatsWithCredits(200, false, true)

	saved, err := LoadUser("Finity", nil)
	if err != nil {
		t.Fatalf("LoadUser(saved) error = %v", err)
	}
	if saved.DayDn.Files != 2 || saved.DayDn.Bytes != 300 {
		t.Fatalf("saved DayDn = %+v, want 2 files / 300 bytes", saved.DayDn)
	}
	if saved.WkDn.Files != 2 || saved.WkDn.Bytes != 300 {
		t.Fatalf("saved WkDn = %+v, want 2 files / 300 bytes", saved.WkDn)
	}
	if saved.MonthDn.Files != 2 || saved.MonthDn.Bytes != 300 {
		t.Fatalf("saved MonthDn = %+v, want 2 files / 300 bytes", saved.MonthDn)
	}
	if saved.AllDn.Files != 2 || saved.AllDn.Bytes != 300 {
		t.Fatalf("saved AllDn = %+v, want 2 files / 300 bytes", saved.AllDn)
	}
	if saved.Credits != 700 {
		t.Fatalf("saved Credits = %d, want 700", saved.Credits)
	}
}
