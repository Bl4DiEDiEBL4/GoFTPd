package core

import (
	"fmt"
	"path"
	"strings"
	"time"
)

const siteSearchLimit = 100

func (s *Session) HandleSiteSearch(args []string) bool {
	if len(args) == 0 || strings.TrimSpace(strings.Join(args, " ")) == "" {
		fmt.Fprintf(s.Conn, "501 Usage: SITE SEARCH <release>\r\n")
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE SEARCH is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	query := strings.TrimSpace(strings.Join(args, " "))
	rawResults := bridge.SearchDirs(query, siteSearchLimit)
	results := make([]VFSSearchResult, 0, len(rawResults))
	for _, result := range rawResults {
		aclPath := path.Join(s.Config.ACLBasePath, result.Path)
		if s.ACLEngine != nil && !s.ACLEngine.CanPerform(s.User, "LIST", aclPath) {
			continue
		}
		results = append(results, result)
	}

	fmt.Fprintf(s.Conn, "200- (Values displayed after dir names are Files/Megs/Age)\r\n")
	fmt.Fprintf(s.Conn, "200- Doing case-insensitive search for '%s':\r\n", query)
	for _, result := range results {
		fmt.Fprintf(s.Conn, "200- %s (%dF/%s/%s)\r\n",
			result.Path,
			result.Files,
			formatSearchMB(result.Bytes),
			formatSearchAge(result.ModTime),
		)
	}
	fmt.Fprintf(s.Conn, "200-  \r\n")
	if len(results) == 1 {
		fmt.Fprintf(s.Conn, "200 1 directory found.\r\n")
	} else {
		fmt.Fprintf(s.Conn, "200 %d directories found.\r\n", len(results))
	}
	return false
}

func formatSearchMB(bytes int64) string {
	return fmt.Sprintf("%.1fM", float64(bytes)/1024.0/1024.0)
}

func formatSearchAge(modTime int64) string {
	if modTime <= 0 {
		return "unknown"
	}
	d := time.Since(time.Unix(modTime, 0))
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		hours := int(d.Hours())
		minutes := int(d.Minutes()) % 60
		return fmt.Sprintf("%dh %02dm", hours, minutes)
	}
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	return fmt.Sprintf("%dd %02dh", days, hours)
}
