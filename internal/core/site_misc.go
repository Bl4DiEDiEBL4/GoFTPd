package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

func (s *Session) HandleSiteChmod(args []string) bool {
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHMOD <mode> <file>\r\n")
		return false
	}
	mode, _ := strconv.ParseUint(args[0], 8, 32)
	fullPath := filepath.Join(s.Config.StoragePath, s.CurrentDir, args[1])
	os.Chmod(fullPath, os.FileMode(mode))
	fmt.Fprintf(s.Conn, "200 SITE CHMOD successful.\r\n")
	return false
}

func (s *Session) HandleSiteXDupe(args []string) bool {
	if len(args) > 0 {
		fmt.Fprintf(s.Conn, "200 XDUPE %s activated.\r\n", args[0])
	} else {
		fmt.Fprintf(s.Conn, "200 XDUPE 3 activated.\r\n")
	}
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