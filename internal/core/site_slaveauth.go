package core

import (
	"fmt"
	"sort"
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
	entry := strings.TrimSpace(args[0])
	removed, err := bridge.RemoveSlaveAuthDenyEntry(entry)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 SLAVEUNBAN failed: %v\r\n", err)
		return false
	}
	cleared, err := bridge.ClearSlaveAuthTempBan(entry)
	if err != nil {
		fmt.Fprintf(s.Conn, "550 SLAVEUNBAN failed: %v\r\n", err)
		return false
	}
	if !removed && !cleared {
		fmt.Fprintf(s.Conn, "550 Entry not found in slave control denylist or active temp bans.\r\n")
		return false
	}
	if removed && cleared {
		fmt.Fprintf(s.Conn, "200 Removed entry from slave control denylist and cleared active temp ban.\r\n")
	} else if removed {
		fmt.Fprintf(s.Conn, "200 Removed entry from slave control denylist.\r\n")
	} else {
		fmt.Fprintf(s.Conn, "200 Cleared active slave temp ban.\r\n")
	}
	return false
}

func (s *Session) HandleSiteSlaveClearBan(args []string) bool {
	if len(args) != 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVECLEARBAN <ip|cidr>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SLAVECLEARBAN is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}
	cleared, err := bridge.ClearSlaveAuthTempBan(strings.TrimSpace(args[0]))
	if err != nil {
		fmt.Fprintf(s.Conn, "550 SLAVECLEARBAN failed: %v\r\n", err)
		return false
	}
	if !cleared {
		fmt.Fprintf(s.Conn, "550 Entry not found in active slave temp bans.\r\n")
		return false
	}
	fmt.Fprintf(s.Conn, "200 Cleared active slave temp ban.\r\n")
	return false
}

// HandleSiteSlaveMask implements the mandatory-when-no-mTLS fallback
// authentication for the master-slave link: an IP/CIDR/wildcard allowlist
// per slave name, in the style of drftpd's "site slave <name> addmask <mask>".
//
//	SITE SLAVE <name> ADDMASK <mask>
//	SITE SLAVE <name> DELMASK <mask>
//	SITE SLAVE <name> MASKS
//	SITE SLAVE MASKS               (lists every slave's masks)
func (s *Session) HandleSiteSlaveMask(args []string) bool {
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SLAVE is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	if len(args) == 1 && strings.EqualFold(args[0], "MASKS") {
		all := bridge.ListAllSlaveMasks()
		if len(all) == 0 {
			fmt.Fprintf(s.Conn, "200 No slave masks registered.\r\n")
			return false
		}
		names := make([]string, 0, len(all))
		for name := range all {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Fprintf(s.Conn, "200- Slave IP masks:\r\n")
		for _, name := range names {
			masks := all[name]
			if len(masks) == 0 {
				fmt.Fprintf(s.Conn, "200-   %s: (none)\r\n", name)
				continue
			}
			fmt.Fprintf(s.Conn, "200-   %s: %s\r\n", name, strings.Join(masks, ", "))
		}
		fmt.Fprintf(s.Conn, "200 End of SLAVE MASKS\r\n")
		return false
	}

	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVE <name> ADDMASK|DELMASK <mask>, or SITE SLAVE <name> MASKS\r\n")
		return false
	}

	name := args[0]
	sub := strings.ToUpper(strings.TrimSpace(args[1]))

	switch sub {
	case "MASKS":
		masks := bridge.ListSlaveMasks(name)
		if len(masks) == 0 {
			fmt.Fprintf(s.Conn, "200 No masks registered for slave %s.\r\n", name)
			return false
		}
		fmt.Fprintf(s.Conn, "200 Masks for slave %s: %s\r\n", name, strings.Join(masks, ", "))
		return false
	case "ADDMASK":
		if len(args) != 3 {
			fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVE <name> ADDMASK <ip|cidr|wildcard>\r\n")
			return false
		}
		if err := bridge.AddSlaveMask(name, args[2]); err != nil {
			fmt.Fprintf(s.Conn, "550 ADDMASK failed: %v\r\n", err)
			return false
		}
		fmt.Fprintf(s.Conn, "200 Added mask %s for slave %s.\r\n", args[2], name)
		return false
	case "DELMASK":
		if len(args) != 3 {
			fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVE <name> DELMASK <ip|cidr|wildcard>\r\n")
			return false
		}
		removed, err := bridge.RemoveSlaveMask(name, args[2])
		if err != nil {
			fmt.Fprintf(s.Conn, "550 DELMASK failed: %v\r\n", err)
			return false
		}
		if !removed {
			fmt.Fprintf(s.Conn, "550 Mask %s not found for slave %s.\r\n", args[2], name)
			return false
		}
		fmt.Fprintf(s.Conn, "200 Removed mask %s for slave %s.\r\n", args[2], name)
		return false
	default:
		fmt.Fprintf(s.Conn, "501 Usage: SITE SLAVE <name> ADDMASK|DELMASK <mask>, or SITE SLAVE <name> MASKS\r\n")
		return false
	}
}
