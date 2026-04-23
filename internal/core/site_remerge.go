package core

import (
	"fmt"
	"strings"
)

func (s *Session) HandleSiteRemerge(args []string) bool {
	if len(args) != 1 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE REMERGE <slave|*>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE REMERGE is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	target := strings.TrimSpace(args[0])
	if target == "*" {
		started, errs := bridge.StartRemergeAll()
		if started == 0 && len(errs) > 0 {
			fmt.Fprintf(s.Conn, "550 REMERGE failed: %s\r\n", strings.Join(errs, "; "))
			return false
		}
		if len(errs) > 0 {
			fmt.Fprintf(s.Conn, "200 REMERGE started for %d slave(s); skipped: %s\r\n", started, strings.Join(errs, "; "))
			return false
		}
		fmt.Fprintf(s.Conn, "200 REMERGE started for %d slave(s).\r\n", started)
		return false
	}

	if err := bridge.StartRemerge(target); err != nil {
		fmt.Fprintf(s.Conn, "550 REMERGE failed: %v\r\n", err)
		return false
	}
	fmt.Fprintf(s.Conn, "200 REMERGE started for %s.\r\n", target)
	return false
}
