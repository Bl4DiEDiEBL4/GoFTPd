package core

import (
	"fmt"
	"log"
	"strings"

	"goftpd/internal/plugin"
)

// DispatchSiteCommand routes SITE sub-commands to their specific handlers.
// This keeps commands.go clean and focused on standard FTP/FXP protocol.
func (s *Session) DispatchSiteCommand(args []string) bool {
	if len(args) == 0 {
		fmt.Fprintf(s.Conn, "500 SITE what?\r\n")
		return false
	}

	siteCmd := strings.ToUpper(args[0])
	remainingArgs := args[1:]

	if s.Config.Debug {
		log.Printf("[SITE] siteCmd=%q remainingArgs=%q", siteCmd, remainingArgs)
	}

	switch siteCmd {
	// Informational Commands (site_info.go)
	case "HELP":
		return s.HandleSiteHelp(remainingArgs)
	case "RULES":
		return s.HandleSiteRules(remainingArgs)
	case "WHO":
		return s.HandleSiteWho(remainingArgs)

	// Admin / User & Group Management (site_admin.go)
	case "ADDUSER":
		return s.HandleSiteAddUser(remainingArgs)
	case "DELUSER":
		return s.HandleSiteDelUser(remainingArgs)
	case "CHPASS":
		return s.HandleSiteChPass(remainingArgs)
	case "ADDIP":
		return s.HandleSiteAddIP(remainingArgs)
	case "DELIP":
		return s.HandleSiteDelIP(remainingArgs)
	case "FLAGS":
		return s.HandleSiteFlags(remainingArgs)
	case "CHGRP":
		return s.HandleSiteChGrp(remainingArgs)
	case "CHPGRP":
		return s.HandleSiteChPGrp(remainingArgs)
	case "GADMIN":
		return s.HandleSiteGAdmin(remainingArgs)
	case "GRPADD":
		return s.HandleSiteGrpAdd(remainingArgs)
	case "GRPDEL":
		return s.HandleSiteGrpDel(remainingArgs)
	case "INVITE":
		return s.HandleSiteInvite(remainingArgs)

	// Release Management (site_nuke.go)
	case "NUKE":
		return s.HandleSiteNuke(remainingArgs)
	case "UNNUKE":
		return s.HandleSiteUnnuke(remainingArgs)
	case "REHASH":
		return s.HandleSiteRehash(remainingArgs)

	// Miscellaneous (site_misc.go / site_race.go)
	case "RACE":
		return s.HandleSiteRace(remainingArgs)
	case "SEARCH":
		return s.HandleSiteSearch(remainingArgs)
	case "RESCAN":
		return s.HandleSiteRescan(remainingArgs)
	case "CHMOD":
		return s.HandleSiteChmod(remainingArgs)
	case "XDUPE":
		return s.HandleSiteXDupe(remainingArgs)
	case "GRP":
		return s.HandleSiteGrp(remainingArgs)

	default:
		if s.Config != nil && s.Config.PluginManager != nil {
			if s.Config.PluginManager.DispatchSiteCommand(pluginSiteContext{s: s}, siteCmd, remainingArgs) {
				return false
			}
		}
		fmt.Fprintf(s.Conn, "504 Unknown SITE command.\r\n")
	}
	return false
}

type pluginSiteContext struct {
	s *Session
}

var _ plugin.SiteContext = pluginSiteContext{}

func (c pluginSiteContext) Reply(format string, args ...interface{}) {
	if c.s == nil || c.s.Conn == nil {
		return
	}
	fmt.Fprintf(c.s.Conn, format, args...)
}

func (c pluginSiteContext) UserName() string {
	if c.s == nil || c.s.User == nil {
		return ""
	}
	return c.s.User.Name
}

func (c pluginSiteContext) UserFlags() string {
	if c.s == nil || c.s.User == nil {
		return ""
	}
	return c.s.User.Flags
}

func (c pluginSiteContext) UserPrimaryGroup() string {
	if c.s == nil || c.s.User == nil {
		return ""
	}
	return c.s.User.PrimaryGroup
}

func (c pluginSiteContext) UserGroups() []string {
	if c.s == nil || c.s.User == nil {
		return nil
	}
	groups := make([]string, 0, len(c.s.User.Groups)+1)
	if c.s.User.PrimaryGroup != "" {
		groups = append(groups, c.s.User.PrimaryGroup)
	}
	for group := range c.s.User.Groups {
		if !containsStringFold(groups, group) {
			groups = append(groups, group)
		}
	}
	return groups
}

func containsStringFold(values []string, needle string) bool {
	for _, value := range values {
		if strings.EqualFold(value, needle) {
			return true
		}
	}
	return false
}
