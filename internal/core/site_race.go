package core

import (
	"fmt"
	"io"
	"path"
	"strings"
)

func (s *Session) HandleSiteRace(args []string) bool {
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE RACE is only available in master mode.\r\n")
		return false
	}

	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	dirPath := s.CurrentDir
	if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
		target := args[0]
		if !strings.HasPrefix(target, "/") {
			target = path.Join(s.CurrentDir, target)
		}
		dirPath = path.Clean(target)
	}

	users, groups, totalBytes, present, total := bridge.GetVFSRaceStats(dirPath)
	users = trimRaceUsers(s.Config, users)
	groups = trimRaceGroups(s.Config, groups)
	if !HasRaceStats(users, groups, totalBytes, present, total) {
		fmt.Fprintf(s.Conn, "200 No race stats for %s\r\n", dirPath)
		return false
	}

	RenderFTPReplyBlock(s.Conn, 250, "Race stats complete.", func(w io.Writer) {
		RenderRaceStats(w, users, groups, totalBytes, present, total, s.Config.Version)
	})
	return false
}
