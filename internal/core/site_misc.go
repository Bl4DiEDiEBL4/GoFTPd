package core

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

type chmodBridge interface {
	Chmod(path string, mode uint32) error
}

func (s *Session) HandleSiteChmod(args []string) bool {
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHMOD <mode> <file>\r\n")
		return false
	}
	mode, err := strconv.ParseUint(args[0], 8, 32)
	if err != nil {
		fmt.Fprintf(s.Conn, "501 Invalid mode (use octal, e.g. 755).\r\n")
		return false
	}
	vpath := path.Join(s.CurrentDir, args[1])
	aclPath := path.Join(s.Config.ACLBasePath, vpath)
	if s.ACLEngine == nil || !s.ACLEngine.CanPerform(s.User, "CHMOD", aclPath) {
		fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
		return false
	}
	if s.Config.Mode == "master" && s.MasterManager != nil {
		if bridge, ok := s.MasterManager.(chmodBridge); ok {
			if err := bridge.Chmod(vpath, uint32(mode)); err != nil {
				fmt.Fprintf(s.Conn, "550 CHMOD failed: %v\r\n", err)
				return false
			}
			fmt.Fprintf(s.Conn, "200 SITE CHMOD successful.\r\n")
			return false
		}
	}
	fullPath := filepath.Join(s.Config.StoragePath, filepath.FromSlash(strings.TrimPrefix(vpath, "/")))
	if err := os.Chmod(fullPath, os.FileMode(mode)); err != nil {
		fmt.Fprintf(s.Conn, "550 CHMOD failed: %v\r\n", err)
		return false
	}
	fmt.Fprintf(s.Conn, "200 SITE CHMOD successful.\r\n")
	return false
}

func (s *Session) HandleSiteXDupe(args []string) bool {
	if len(args) == 0 {
		if s.XDupeMode == 0 {
			fmt.Fprintf(s.Conn, "200 Extended dupe mode is disabled.\r\n")
		} else {
			fmt.Fprintf(s.Conn, "200 Extended dupe mode %d is enabled.\r\n", s.XDupeMode)
		}
		return false
	}

	mode, err := strconv.Atoi(strings.TrimSpace(args[0]))
	if err != nil {
		fmt.Fprintf(s.Conn, "501 Syntax error.\r\n")
		return false
	}
	if mode < 0 || mode > 4 {
		fmt.Fprintf(s.Conn, "504 Command not implemented for that parameter.\r\n")
		return false
	}
	s.XDupeMode = mode
	fmt.Fprintf(s.Conn, "200 Activated extended dupe mode %d.\r\n", mode)
	return false
}

func (s *Session) HandleSiteGrp(args []string) bool {
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "200- Groups:\r\n")
		for gName, gID := range s.GroupMap {
			fmt.Fprintf(s.Conn, "200- %-15s GID: %3d\r\n", gName, gID)
		}
		fmt.Fprintf(s.Conn, "200 End\r\n")
		return false
	}
	groupName := args[0]
	if gid, ok := s.GroupMap[groupName]; ok {
		fmt.Fprintf(s.Conn, "200- Group: %s (GID: %d)\r\n", groupName, gid)
		fmt.Fprintf(s.Conn, "200 End\r\n")
	} else {
		fmt.Fprintf(s.Conn, "550 Group not found.\r\n")
	}
	return false
}
