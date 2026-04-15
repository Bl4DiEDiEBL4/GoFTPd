package core

import (
	"fmt"
	"log"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// HandleSiteInvite handles SITE INVITE username command
func (s *Session) HandleSiteInvite(args []string) bool {
	if len(args) < 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE INVITE <username>\r\n")
		return false
	}

	// Get channels from sitebot config
	channels := s.getSitebotChannels()
	if len(channels) == 0 {
		fmt.Fprintf(s.Conn, "450 Sitebot not configured or no channels available\r\n")
		return false
	}

	if s.Config.Debug {
		log.Printf("[INVITE] User %s invited to sitebot channels", args[0])
	}

	// Return the channels the user should join
	fmt.Fprintf(s.Conn, "200-IRC Channels:\r\n")
	for _, channel := range channels {
		fmt.Fprintf(s.Conn, "200-%s\r\n", channel)
	}
	fmt.Fprintf(s.Conn, "200 Please join these channels with your IRC client!\r\n")

	return false
}

// getSitebotChannels reads channels from sitebot config
func (s *Session) getSitebotChannels() []string {
	// Read sitebot config from plugins/sitebot/etc/config.yml
	configPath := filepath.Join(s.Config.StoragePath, "..", "plugins/sitebot/etc/config.yml")

	data, err := os.ReadFile(configPath)
	if err != nil {
		if s.Config.Debug {
			log.Printf("[INVITE] Could not read sitebot config: %v", err)
		}
		return []string{}
	}

	// Parse YAML to get channels
	var config map[string]interface{}
	if err := yaml.Unmarshal(data, &config); err != nil {
		if s.Config.Debug {
			log.Printf("[INVITE] Could not parse sitebot config: %v", err)
		}
		return []string{}
	}

	// Extract channels from irc section
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
