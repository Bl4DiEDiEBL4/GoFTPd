package core

import (
	"fmt"
	"log"
	"strings"
	"time"
)

func (s *Session) HandleSiteWho(args []string) bool {
	fmt.Fprintf(s.Conn, "200- Currently Online:\r\n")
	for _, snap := range listActiveSessions() {
		user := snap.User
		if user == "" {
			user = "(login)"
		}
		idle := time.Since(snap.StartedAt).Round(time.Second)
		fmt.Fprintf(s.Conn, "200- #%d User: %-12s | Flags: %-8s | IP: %-22s | Dir: %-20s | Online: %s\r\n",
			snap.ID, user, snap.Flags, snap.Remote, snap.CurrentDir, idle)
	}
	fmt.Fprintf(s.Conn, "200 End of WHO\r\n")
	return false
}

func (s *Session) HandleSiteHelp(args []string) bool {

	if s.Config.Debug {
		log.Printf("[SITE HELP] args=%q", args)
	}

	vars := map[string]string{
		"sitename": s.Config.SiteName,
		"version":  s.Config.Version,
		"username": s.User.Name,
	}
	help, err := LoadMessageTemplate("help.msg", vars, s.Config)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Help not available\r\n")
	} else {
		for _, line := range strings.Split(help, "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(s.Conn, "%s\r\n", line)
			}
		}
	}
	return false
}

func (s *Session) HandleSiteRules(args []string) bool {
	vars := map[string]string{
		"sitename": s.Config.SiteName,
		"version":  s.Config.Version,
		"username": s.User.Name,
	}
	rules, err := LoadMessageTemplate("rules.msg", vars, s.Config)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 Rules not available\r\n")
	} else {
		for _, line := range strings.Split(rules, "\n") {
			if strings.TrimSpace(line) != "" {
				fmt.Fprintf(s.Conn, "%s\r\n", line)
			}
		}
	}
	return false
}
