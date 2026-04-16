package core

import (
	"fmt"
	"log"
	"strings"
)

func (s *Session) HandleSiteWho(args []string) bool {
	fmt.Fprintf(s.Conn, "200- Currently Online:\r\n")
	fmt.Fprintf(s.Conn, "200- User: %-10s | Flags: %-10s | IP: %s\r\n", s.User.Name, s.User.Flags, s.Conn.RemoteAddr())
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