package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goftpd/internal/user"
)

func (s *Session) HandleSiteAddUser(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE ADDUSER <name> <pass>\r\n")
		return false
	}
	newUser := &user.User{
		Name:     args[0],
		Password: args[1],
		Flags:    "3",
		Groups:   make(map[string]int),
		Credits:  1024 * 1024 * 100,
		Ratio:    3,
		IPs:      []string{"*@*"},
		Added:    time.Now().Unix(),
	}
	newUser.Save()
	fmt.Fprintf(s.Conn, "200 User %s added.\r\n", args[0])
	return false
}

func (s *Session) HandleSiteGrpAdd(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE GRPADD <groupname> [description]\r\n")
		return false
	}
	groupName := args[0]
	desc := groupName
	if len(args) > 1 {
		desc = strings.Join(args[1:], " ")
	}
	nextGID := 100
	for _, gid := range s.GroupMap {
		if gid >= nextGID { nextGID = gid + 100 }
	}
	groupPath := filepath.Join("etc", "groups", groupName)
	groupContent := fmt.Sprintf("GROUP %s\nSLOTS -1 0 0 0\nGROUPNFO %s\nSIMULT 0\n", groupName, desc)
	os.WriteFile(groupPath, []byte(groupContent), 0644)
	s.GroupMap[groupName] = nextGID
	AddGroupToFile(groupName, desc, nextGID)
	fmt.Fprintf(s.Conn, "200 Group %s added.\r\n", groupName)
	return false
}

func (s *Session) HandleSiteGrpDel(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE GRPDEL <groupname>\r\n")
		return false
	}
	groupName := args[0]
	os.Remove(filepath.Join("etc", "groups", groupName))
	delete(s.GroupMap, groupName)
	fmt.Fprintf(s.Conn, "200 Group %s deleted.\r\n", groupName)
	return false
}

func (s *Session) HandleSiteChGrp(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHGRP <user> <group>\r\n")
		return false
	}
	targetUser, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User not found.\r\n")
		return false
	}
	targetUser.Groups[args[1]] = 0
	targetUser.Save()
	fmt.Fprintf(s.Conn, "200 User %s added to group %s.\r\n", args[0], args[1])
	return false
}

func (s *Session) HandleSiteChPGrp(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHPGRP <user> <group>\r\n")
		return false
	}
	targetUser, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User not found.\r\n")
		return false
	}
	targetUser.PrimaryGroup = args[1]
	if gid, ok := s.GroupMap[args[1]]; ok {
		targetUser.GID = gid
	}
	targetUser.Save()
	fmt.Fprintf(s.Conn, "200 Primary group changed.\r\n")
	return false
}

func (s *Session) HandleSiteGAdmin(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE GADMIN <group> <user>\r\n")
		return false
	}
	targetUser, err := user.LoadUser(args[1], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User not found.\r\n")
		return false
	}
	targetUser.Groups[args[0]] = 1
	targetUser.Save()
	fmt.Fprintf(s.Conn, "200 Gadmin set.\r\n")
	return false
}