package core

import (
	"fmt"
	"log"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// HandleSiteInvite handles SITE INVITE username command.
// Channels are filtered by the inviting user's flags per invite_channels: in
// the main goftpd config. Channels not listed in invite_channels are public.
func (s *Session) HandleSiteInvite(args []string) bool {
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE INVITE <username>\r\n")
		return false
	}

	channels := s.getSitebotChannels()
	if len(channels) == 0 {
		fmt.Fprintf(s.Conn, "450 Sitebot not configured or no channels available\r\n")
		return false
	}

	// Filter channels by the current user's flags.
	allowed := filterInviteChannels(channels, s.Config.InviteChannels, s.User.Flags)
	if len(allowed) == 0 {
		fmt.Fprintf(s.Conn, "450 No channels available for your access level\r\n")
		return false
	}

	if s.Config.Debug {
		log.Printf("[INVITE] %s invited %s to %d channel(s)", s.User.Name, args[0], len(allowed))
	}

	// Emit an INVITE event per channel so the sitebot can /invite the nick.
	// Joined as comma-separated string because Event.Data is map[string]string.
	ircNick := args[0]
	s.emitEvent(EventInvite, "", "", 0, 0, map[string]string{
		"nick":     ircNick,
		"channels": strings.Join(allowed, ","),
		"inviter":  s.User.Name,
	})

	fmt.Fprintf(s.Conn, "200-IRC Channels:\r\n")
	for _, channel := range allowed {
		fmt.Fprintf(s.Conn, "200-%s\r\n", channel)
	}
	fmt.Fprintf(s.Conn, "200 Sitebot has been asked to invite you to these channels.\r\n")

	return false
}

// filterInviteChannels returns only those channels the user's flags allow.
// rules maps a channel name (lowercased) to the flag(s) required to see it.
// A channel with no rule is public (returned for everyone). A channel whose
// rule is empty string is also public. A rule like "1" or "12" means the
// user must have at least one of the listed flag characters.
func filterInviteChannels(channels []string, rules []InviteRule, userFlags string) []string {
	ruleMap := map[string]string{}
	for _, r := range rules {
		ruleMap[strings.ToLower(strings.TrimSpace(r.Channel))] = strings.TrimSpace(r.Flags)
	}
	out := []string{}
	for _, ch := range channels {
		required, hasRule := ruleMap[strings.ToLower(strings.TrimSpace(ch))]
		if !hasRule || required == "" {
			// No rule = public channel
			out = append(out, ch)
			continue
		}
		// Require at least one flag from `required` to appear in userFlags
		if anyFlagMatches(userFlags, required) {
			out = append(out, ch)
		}
	}
	return out
}

func anyFlagMatches(userFlags, required string) bool {
	for _, f := range required {
		if strings.ContainsRune(userFlags, f) {
			return true
		}
	}
	return false
}

// getSitebotChannels reads channels from the sitebot's config.yml.
// The sitebot config is the source of truth — set its path via
// `sitebot_config:` in the main goftpd config.yml.
func (s *Session) getSitebotChannels() []string {
	if s.Config.SitebotConfig == "" {
		if s.Config.Debug {
			log.Printf("[INVITE] sitebot_config not set in main config")
		}
		return []string{}
	}

	data, err := os.ReadFile(s.Config.SitebotConfig)
	if err != nil {
		if s.Config.Debug {
			log.Printf("[INVITE] Could not read sitebot config %s: %v", s.Config.SitebotConfig, err)
		}
		return []string{}
	}

	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		if s.Config.Debug {
			log.Printf("[INVITE] Could not parse sitebot config: %v", err)
		}
		return []string{}
	}

	if ircConfig, ok := config["irc"].(map[string]interface{}); ok {
		if channels, ok := ircConfig["channels"].([]interface{}); ok {
			var result []string
			for _, ch := range channels {
				if chanStr, ok := ch.(string); ok {
					result = append(result, chanStr)
				}
			}
			return result
		}
	}
	return []string{}
}
