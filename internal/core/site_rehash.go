package core

import (
	"fmt"
	"log"
)

// HandleSiteRehash handles SITE REHASH — reloads the main goftpd config
// from disk. Restricted to siteops (flag 1). Fields that require process
// restart (listen ports, TLS certs, storage path, mode) are not reloaded.
func (s *Session) HandleSiteRehash(args []string) bool {
	if s.User == nil || !s.User.HasFlag("1") {
		fmt.Fprintf(s.Conn, "550 Access denied: siteop only.\r\n")
		return false
	}

	path, err := s.Config.Rehash()
	if err != nil {
		log.Printf("[REHASH] %s failed: %v", s.User.Name, err)
		fmt.Fprintf(s.Conn, "550 Rehash failed: %v\r\n", err)
		return false
	}

	log.Printf("[REHASH] %s reloaded config from %s", s.User.Name, path)
	fmt.Fprintf(s.Conn, "200- Reloaded: %s\r\n", path)
	fmt.Fprintf(s.Conn, "200- Reloaded: etc/permissions.yml\r\n")
	fmt.Fprintf(s.Conn, "200- Reloaded: plugin config_file entries referenced from %s\r\n", path)
	fmt.Fprintf(s.Conn, "200- Applied: zipscript, plugin blocks, invite/sitebot settings, sections, slave policies, limits, logging\r\n")
	fmt.Fprintf(s.Conn, "200- (port, TLS, storage_path, mode require restart)\r\n")
	fmt.Fprintf(s.Conn, "200 Rehash complete.\r\n")
	return false
}
