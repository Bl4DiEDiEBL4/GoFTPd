package core

import (
	"fmt"
	"strings"
	"time"
)

func (s *Session) HandleSiteSlaveBans(args []string) bool {
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SLAVEBANS is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	deny := bridge.ListSlaveAuthDenyEntries()
	temp := bridge.ListSlaveAuthTempBans()

	fmt.Fprintf(s.Conn, "200- Slave control denylist:\r\n")
	if len(deny) == 0 {
		fmt.Fprintf(s.Conn, "200-   (empty)\r\n")
	} else {
		for _, entry := range deny {
			fmt.Fprintf(s.Conn, "200-   deny  %s\r\n", entry)
		}
	}
	fmt.Fprintf(s.Conn, "200- Active temp bans:\r\n")
	if len(temp) == 0 {
		fmt.Fprintf(s.Conn, "200-   (none)\r\n")
	} else {
		now := time.Now()
		for _, entry := range temp {
			remaining := entry.BannedUntil.Sub(now).Round(time.Second)
			if remaining < 0 {
				remaining = 0
			}
			fmt.Fprintf(s.Conn, "200-   temp  %s  strikes=%d  until=%s  remaining=%s\r\n",
				entry.IP, entry.Strikes, entry.BannedUntil.Format(time.RFC3339), remaining)
		}
	}
	fmt.Fprintf(s.Conn, "200 End of SLAVEBANS\r\n")
	return false
}

func (s *Session) HandleSiteSlaveBan(args []string) bool {
	if len(args) != 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVEBAN <ip|cidr>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SLAVEBAN is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}
	entry, err := bridge.AddSlaveAuthDenyEntry(strings.TrimSpace(args[0]))
	if err != nil {
		fmt.Fprintf(s.Conn, "550 SLAVEBAN failed: %v\r\n", err)
		return false
	}
	fmt.Fprintf(s.Conn, "200 Added %s to slave control denylist.\r\n", entry)
	return false
}

func (s *Session) HandleSiteSlaveUnban(args []string) bool {
	if len(args) != 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVEUNBAN <ip|cidr>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SLAVEUNBAN is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}
	removed, err := bridge.RemoveSlaveAuthDenyEntry(strings.TrimSpace(args[0]))
	if err != nil {
		fmt.Fprintf(s.Conn, "550 SLAVEUNBAN failed: %v\r\n", err)
		return false
	}
	if !removed {
		fmt.Fprintf(s.Conn, "550 Entry not found in slave control denylist.\r\n")
		return false
	}
	fmt.Fprintf(s.Conn, "200 Removed entry from slave control denylist.\r\n")
	return false
}
