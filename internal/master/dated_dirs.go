package master

import (
	"log"
	"path"
	"strings"
	"time"
)

type DatedDirsConfig struct {
	Enabled              bool
	Sections             []string
	Format               string
	CreateTomorrow       bool
	TodaySymlink         bool
	SymlinkPrefix        string
	ReadOnlyAfterMinutes int
}

func (sm *SlaveManager) StartDatedDirs(cfg DatedDirsConfig) {
	sm.datedMu.Lock()
	sm.datedConfig = normalizeDatedDirsConfig(cfg)
	if sm.datedStarted {
		sm.datedMu.Unlock()
		return
	}
	sm.datedStarted = true
	sm.datedMu.Unlock()

	go sm.datedDirsLoop()
}

func (sm *SlaveManager) datedDirsLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	sm.applyDatedDirs(time.Now())
	for sm.running.Load() {
		<-ticker.C
		sm.applyDatedDirs(time.Now())
	}
}

func (sm *SlaveManager) applyDatedDirs(now time.Time) {
	sm.datedMu.Lock()
	cfg := sm.datedConfig
	sm.datedMu.Unlock()
	if !cfg.Enabled {
		return
	}

	today := now.Format(cfg.Format)
	tomorrow := now.AddDate(0, 0, 1).Format(cfg.Format)
	yesterday := now.AddDate(0, 0, -1).Format(cfg.Format)

	for _, section := range cfg.Sections {
		section = strings.Trim(strings.TrimSpace(section), "/")
		if section == "" {
			continue
		}

		todayPath := "/" + section + "/" + today
		sm.ensureDatedDir(todayPath, 0777)
		if cfg.CreateTomorrow {
			sm.ensureDatedDir("/"+section+"/"+tomorrow, 0777)
		}
		if cfg.TodaySymlink {
			linkPath := "/" + cfg.SymlinkPrefix + section
			if err := sm.ensureSymlink(linkPath, todayPath); err != nil {
				log.Printf("[DATED] symlink %s -> %s failed: %v", linkPath, todayPath, err)
			}
		}

		if minutesSinceMidnight(now) >= cfg.ReadOnlyAfterMinutes {
			sm.chmodPath("/"+section+"/"+yesterday, 0555)
		}
	}
}

func (sm *SlaveManager) ensureDatedDir(dirPath string, mode uint32) {
	sm.vfs.AddFile(dirPath, VFSFile{
		Path:         dirPath,
		IsDir:        true,
		Mode:         mode,
		LastModified: time.Now().Unix(),
		Owner:        "GoFTPd",
		Group:        "GoFTPd",
		Seen:         true,
	})

	for _, slave := range sm.slavesForDatedPath(dirPath) {
		index, err := IssueMakeDir(slave, dirPath)
		if err == nil {
			_, _ = slave.FetchResponse(index, 30*time.Second)
		}
		index, err = IssueChmod(slave, dirPath, mode)
		if err == nil {
			_, _ = slave.FetchResponse(index, 30*time.Second)
		}
	}
}

func (sm *SlaveManager) chmodPath(dirPath string, mode uint32) {
	sm.vfs.Chmod(dirPath, mode)
	for _, slave := range sm.slavesForDatedPath(dirPath) {
		index, err := IssueChmod(slave, dirPath, mode)
		if err == nil {
			_, _ = slave.FetchResponse(index, 30*time.Second)
		}
	}
}

func (sm *SlaveManager) ensureSymlink(linkPath, targetPath string) error {
	sm.vfs.AddSymlink(linkPath, targetPath)
	var lastErr error
	for _, slave := range sm.slavesForDatedPath(targetPath) {
		targetArg := strings.TrimPrefix(path.Clean(targetPath), "/")
		index, err := IssueSymlink(slave, linkPath, targetArg)
		if err != nil {
			lastErr = err
			continue
		}
		if _, err := slave.FetchResponse(index, 30*time.Second); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

func (sm *SlaveManager) slavesForDatedPath(dirPath string) []*RemoteSlave {
	section := sectionFromUploadPath(dirPath)
	all := sm.GetAvailableSlaves()
	out := make([]*RemoteSlave, 0, len(all))
	for _, slave := range all {
		policy, hasPolicy := sm.getPolicy(slave.Name())
		if !hasPolicy || slavePolicyAccepts(policy, section, dirPath) {
			out = append(out, slave)
		}
	}
	if len(out) == 0 {
		return all
	}
	return out
}

func normalizeDatedDirsConfig(cfg DatedDirsConfig) DatedDirsConfig {
	if cfg.Format == "" {
		cfg.Format = "0102"
	} else {
		cfg.Format = strings.NewReplacer(
			"%Y", "2006",
			"%y", "06",
			"%m", "01",
			"%d", "02",
		).Replace(cfg.Format)
	}
	if cfg.SymlinkPrefix == "" {
		cfg.SymlinkPrefix = "!Today_"
	}
	if cfg.ReadOnlyAfterMinutes <= 0 {
		cfg.ReadOnlyAfterMinutes = 60
	}
	return cfg
}

func minutesSinceMidnight(t time.Time) int {
	return t.Hour()*60 + t.Minute()
}
