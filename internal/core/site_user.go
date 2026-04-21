package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"goftpd/internal/user"
)

const defaultUserTemplate = "etc/users/default.user"

func (s *Session) HandleSiteAddUser(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE ADDUSER <n> <pass> [ident@ip ...]\r\n")
		return false
	}

	// Check if user already exists
	if _, err := user.LoadUser(args[0], s.GroupMap); err == nil {
		fmt.Fprintf(s.Conn, "550 User %s already exists. Use SITE CHPASS or SITE ADDIP.\r\n", args[0])
		return false
	}

	hashedPass, err := HashPassword(args[1])
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Failed to hash password: %v\r\n", err)
		return false
	}

	var ips []string
	if len(args) > 2 {
		for _, ip := range args[2:] {
			if !strings.Contains(ip, "@") {
				ip = "*@" + ip
			}
			ips = append(ips, ip)
		}
	}
	if len(ips) == 0 {
		ips = []string{"*@*"}
	}

	newUser, err := user.LoadTemplate(args[0], defaultUserTemplate, s.GroupMap)
	if err != nil {
		newUser = &user.User{
			Name:         args[0],
			Flags:        "3",
			Tagline:      "No Tagline Set",
			HomeRoot:     "/site",
			HomeDir:      "/",
			Groups:       map[string]int{"NoGroup": 0},
			PrimaryGroup: "NoGroup",
			Credits:      15000,
			Ratio:        3,
			UploadSlots:   6,
			DownloadSlots: 3,
		}
	}
	newUser.Name = args[0]
	newUser.Password = hashedPass
	newUser.IPs = ips
	newUser.Added = time.Now().Unix()
	if newUser.Groups == nil {
		newUser.Groups = make(map[string]int)
	}
	if newUser.PrimaryGroup != "" {
		if _, ok := newUser.Groups[newUser.PrimaryGroup]; !ok {
			newUser.Groups[newUser.PrimaryGroup] = 0
		}
	}
	newUser.Save()

	AddUserToPasswd(args[0], hashedPass, s.Config.PasswdFile)

	fmt.Fprintf(s.Conn, "200 User %s added with %d IP(s).\r\n", args[0], len(ips))
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
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHGRP <user> <group> [group2 ...]\r\n")
		return false
	}
	targetUser, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User not found.\r\n")
		return false
	}

	// Toggle group membership (drftpd style): if in group, remove; if not, add
	var added, removed []string
	for _, grp := range args[1:] {
		if _, inGroup := targetUser.Groups[grp]; inGroup {
			delete(targetUser.Groups, grp)
			removed = append(removed, grp)
		} else {
			targetUser.Groups[grp] = 0
			added = append(added, grp)
		}
	}
	targetUser.Save()

	msg := fmt.Sprintf("200 %s:", args[0])
	if len(added) > 0 {
		msg += " added " + strings.Join(added, ",")
	}
	if len(removed) > 0 {
		msg += " removed " + strings.Join(removed, ",")
	}
	fmt.Fprintf(s.Conn, "%s.\r\n", msg)
	return false
}

// HandleSiteFlags adds or removes flags from a user.
// Usage: SITE FLAGS <user> <+|-><flags>
// Examples:
//   SITE FLAGS N0pe +1      (add siteop flag)
//   SITE FLAGS N0pe -1      (remove siteop flag)
//   SITE FLAGS N0pe +1G     (add siteop and gadmin)
//   SITE FLAGS N0pe =13     (replace all flags with 1 and 3)

func (s *Session) HandleSiteFlags(args []string) bool {

	if s.Config.Debug {
		log.Printf("[SITE FLAGS] args=%q len=%d", args, len(args))
	}

	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE FLAGS <user> <+|-|=><flags>\r\n")
		return false
	}

	targetUser, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User %s not found.\r\n", args[0])
		return false
	}

	op := args[1][0]
	if op != '+' && op != '-' && op != '=' {
		fmt.Fprintf(s.Conn, "501 First char must be +, -, or =\r\n")
		return false
	}
	flags := args[1][1:]

	switch op {
	case '=':
		targetUser.Flags = flags
	case '+':
		for _, f := range flags {
			if !strings.ContainsRune(targetUser.Flags, f) {
				targetUser.Flags += string(f)
			}
		}
	case '-':
		for _, f := range flags {
			targetUser.Flags = strings.ReplaceAll(targetUser.Flags, string(f), "")
		}
	}

	targetUser.Save()
	fmt.Fprintf(s.Conn, "200 Flags for %s: %s\r\n", args[0], targetUser.Flags)
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
// HandleSiteChPass changes a user's password.
// Usage: SITE CHPASS <user> <newpass>
func (s *Session) HandleSiteChPass(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE CHPASS <user> <newpass>\r\n")
		return false
	}
	
	u, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User %s not found.\r\n", args[0])
		return false
	}
	
	hashedPass, err := HashPassword(args[1])
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Failed to hash password: %v\r\n", err)
		return false
	}
	
	u.Password = hashedPass
	u.Save()
	AddUserToPasswd(args[0], hashedPass, s.Config.PasswdFile)
	
	fmt.Fprintf(s.Conn, "200 Password changed for %s.\r\n", args[0])
	return false
}

// HandleSiteAddIP adds one or more IPs to an existing user.
// Usage: SITE ADDIP <user> <ident@ip> [ident@ip ...]
func (s *Session) HandleSiteAddIP(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE ADDIP <user> <ident@ip> [ident@ip ...]\r\n")
		return false
	}
	
	u, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User %s not found.\r\n", args[0])
		return false
	}
	
	added := 0
	for _, ip := range args[1:] {
		if !strings.Contains(ip, "@") {
			ip = "*@" + ip
		}
		// Skip if already present
		exists := false
		for _, existing := range u.IPs {
			if existing == ip {
				exists = true
				break
			}
		}
		if !exists {
			u.IPs = append(u.IPs, ip)
			added++
		}
	}
	
	u.Save()
	fmt.Fprintf(s.Conn, "200 Added %d IP(s) to %s (total: %d).\r\n", added, args[0], len(u.IPs))
	return false
}

// HandleSiteDelIP removes one or more IPs from a user.
// Usage: SITE DELIP <user> <ident@ip> [ident@ip ...]
func (s *Session) HandleSiteDelIP(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE DELIP <user> <ident@ip> [ident@ip ...]\r\n")
		return false
	}
	
	u, err := user.LoadUser(args[0], s.GroupMap)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 User %s not found.\r\n", args[0])
		return false
	}
	
	removed := 0
	for _, ip := range args[1:] {
		if !strings.Contains(ip, "@") {
			ip = "*@" + ip
		}
		for i, existing := range u.IPs {
			if existing == ip {
				u.IPs = append(u.IPs[:i], u.IPs[i+1:]...)
				removed++
				break
			}
		}
	}
	
	u.Save()
	fmt.Fprintf(s.Conn, "200 Removed %d IP(s) from %s (remaining: %d).\r\n", removed, args[0], len(u.IPs))
	return false
}

// HandleSiteDelUser deletes a user account.
// Usage: SITE DELUSER <user>
func (s *Session) HandleSiteDelUser(args []string) bool {
	if !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied.\r\n")
		return false
	}
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE DELUSER <user>\r\n")
		return false
	}
	if args[0] == s.User.Name {
		fmt.Fprintf(s.Conn, "550 Cannot delete yourself.\r\n")
		return false
	}
	
	// Delete user file
	userPath := filepath.Join("etc", "users", args[0])
	if err := os.Remove(userPath); err != nil {
		fmt.Fprintf(s.Conn, "550 User %s not found.\r\n", args[0])
		return false
	}
	
	// Remove from passwd file
	RemoveUserFromPasswd(args[0], s.Config.PasswdFile)
	
	fmt.Fprintf(s.Conn, "200 User %s deleted.\r\n", args[0])
	return false
}
