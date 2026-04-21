package core

import (
	"crypto/tls"
	"fmt"
	"io/fs"
	"log"
	"net"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"goftpd/internal/user"
)

// getMlsdPerm returns MLSD permissions string for a file
func getMlsdPerm(info os.FileInfo, isSymlink bool) string {
	perms := ""
	if isSymlink {
		perms = "flr"
	} else if info.IsDir() {
		perms = "flcdmpe"
	} else {
		perms = "flrwd"
	}
	return perms
}

// isSceneSubfolder returns true if name is a conventional subfolder that lives
// INSIDE a scene release (not a release itself). These are created repeatedly
// across every release and must not trigger dupe-check.
func isSceneSubfolder(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "sample", "samples", "proof", "proofs", "subs", "sub", "subtitles",
		"cover", "covers", "covers-back", "covers-front", "covers-side",
		"extras", "extra", "featurettes", "nfo":
		return true
	}
	// CD1, CD2, ..., DVD1, DVD2, DISC1, etc.
	if m, _ := regexp.MatchString(`^(cd|dvd|disc|disk)\d+$`, lower); m {
		return true
	}
	return false
}

// processCommand handles the core RFC 959 FTP and FXP commands.
func (s *Session) processCommand(cmd string, args []string, tlsConfig *tls.Config) bool {
	switch cmd {
	case "FEAT":
		fmt.Fprintf(s.Conn, "211- Extensions supported:\r\n")
		fmt.Fprintf(s.Conn, " AUTH TLS\r\n")
		fmt.Fprintf(s.Conn, " PBSZ\r\n")
		fmt.Fprintf(s.Conn, " PROT\r\n")
		fmt.Fprintf(s.Conn, " SIZE\r\n")
		fmt.Fprintf(s.Conn, " MDTM\r\n")
		fmt.Fprintf(s.Conn, " MLSD\r\n")
		fmt.Fprintf(s.Conn, " MLST Type*;Size*;Modify*;Perm*;\r\n")
		fmt.Fprintf(s.Conn, " REST STREAM\r\n")
		fmt.Fprintf(s.Conn, " SSCN\r\n")
		fmt.Fprintf(s.Conn, " CPSV\r\n")
		fmt.Fprintf(s.Conn, " PRET\r\n")
		fmt.Fprintf(s.Conn, " SITE\r\n")
		fmt.Fprintf(s.Conn, " UTF8\r\n")
		fmt.Fprintf(s.Conn, "211 End\r\n")

	case "OPTS":
		if len(args) > 0 && strings.ToUpper(args[0]) == "UTF8" {
			fmt.Fprintf(s.Conn, "200 UTF8 set to on\r\n")
		} else {
			fmt.Fprintf(s.Conn, "200 OPTS accepted.\r\n")
		}

	case "PBSZ":
		fmt.Fprintf(s.Conn, "200 PBSZ 0 successful\r\n")

	case "PROT":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		if strings.ToUpper(args[0]) == "P" {
			s.DataTLS = true
			fmt.Fprintf(s.Conn, "200 Protection set to Private\r\n")
		} else {
			s.DataTLS = false
			fmt.Fprintf(s.Conn, "200 Protection set to Clear\r\n")
		}

	case "SSCN":
		if len(args) > 0 && strings.ToUpper(args[0]) == "ON" {
			s.SSCN = true
			fmt.Fprintf(s.Conn, "200 SSCN enabled. Ready for secure FXP.\r\n")
		} else {
			s.SSCN = false
			fmt.Fprintf(s.Conn, "200 SSCN disabled.\r\n")
		}

	case "CPSV":
		if s.Config.Debug {
			log.Printf("[CPSV] Starting passive mode setup (passthrough=%v)", s.Config.Passthrough)
		}
		s.SSCN = true

		if s.Config.Passthrough && s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				slaveIP, port, xferIdx, slaveName, err := bridge.SlaveListenForPassthrough(s.CurrentDir)
				if err != nil {
					log.Printf("[CPSV] Passthrough slave listen failed: %v", err)
					fmt.Fprintf(s.Conn, "421 No available slave for passthrough.\r\n")
					return false
				}
				s.PassthruSlave = slaveName
				s.PassthruXferIdx = xferIdx
				if s.DataListen != nil {
					s.DataListen.Close()
					s.DataListen = nil
				}
				ip := strings.ReplaceAll(slaveIP, ".", ",")
				response := fmt.Sprintf("227 Entering Passive Mode (%s,%d,%d)\r\n", ip, port/256, port%256)
				if s.Config.Debug {
					log.Printf("[CPSV] Passthrough to slave %s: %s (port: %d)", slaveName, strings.TrimSpace(response), port)
				}
				fmt.Fprintf(s.Conn, response)
				return false
			}
		}

		var l net.Listener
		var port int
		var err error
		for p := s.Config.PasvMin; p <= s.Config.PasvMax; p++ {
			l, err = net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err == nil {
				port = p
				break
			}
		}
		if l == nil {
			fmt.Fprintf(s.Conn, "421 No available passive ports.\r\n")
			return false
		}
		s.DataListen = l
		s.PassthruSlave = nil
		ip := strings.ReplaceAll(s.Config.PublicIP, ".", ",")
		fmt.Fprintf(s.Conn, "227 Entering Passive Mode (%s,%d,%d)\r\n", ip, port/256, port%256)
		return false

	case "USER":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error.\r\n")
			return false
		}
		s.User = nil
		u, err := user.LoadUser(args[0], s.GroupMap)
		if err != nil {
			if s.Config.Debug {
				log.Printf("[AUTH] Failed to load user '%s': %v", args[0], err)
			}
		} else {
			s.User = u
		}
		fmt.Fprintf(s.Conn, "331 Password required for %s\r\n", args[0])

	case "PASS":
		if s.User != nil {
			remoteIP, _, _ := net.SplitHostPort(s.Conn.RemoteAddr().String())

			if s.Config.IPRestrictions != nil {
				if restrictedIPs, ok := s.Config.IPRestrictions[s.User.Name]; ok && len(restrictedIPs) > 0 {
					allowed := false
					for _, allowedIP := range restrictedIPs {
						if remoteIP == allowedIP || allowedIP == "*" {
							allowed = true
							break
						}
					}
					if !allowed {
						if s.Config.Debug {
							log.Printf("[PASS] User %s login rejected: IP %s not in whitelist", s.User.Name, remoteIP)
						}
						fmt.Fprintf(s.Conn, "530 Login not allowed from this IP.\r\n")
						return false
					}
				}
			}

			if s.User.IsExpired() {
				fmt.Fprintf(s.Conn, "530 Account expired.\r\n")
				return false
			}

			pass := ""
			if len(args) > 0 {
				pass = args[0]
			}

			passwordOK := false
			passwds, err := LoadPasswdFile(s.Config.PasswdFile)
			if err == nil {
				if hash, ok := passwds[s.User.Name]; ok {
					passwordOK = VerifyPassword(pass, hash)
				}
			}
			if !passwordOK && s.User.Password != "" {
				passwordOK = (s.User.Password == pass)
			}
			if !passwordOK {
				fmt.Fprintf(s.Conn, "530 Login incorrect.\r\n")
				return false
			}

			allowed := false
			for _, mask := range s.User.IPs {
				cleanMask := mask
				if strings.Contains(mask, "@") {
					cleanMask = strings.Split(mask, "@")[1]
				}
				if cleanMask == "*" || cleanMask == remoteIP {
					allowed = true
					break
				}
			}
			if !allowed {
				fmt.Fprintf(s.Conn, "530 IP not allowed.\r\n")
				return false
			}

			isTLSExempt := false
			for _, exemptUser := range s.Config.TLSExemptUsers {
				if exemptUser == s.User.Name {
					isTLSExempt = true
					break
				}
			}

			if s.Config.RequireTLSControl && !isTLSExempt && !s.IsTLS {
				if s.Config.Debug {
					log.Printf("[PASS] User %s rejected: TLS required on control channel", s.User.Name)
				}
				fmt.Fprintf(s.Conn, "530 TLS required.\r\n")
				return false
			}

			s.IsLogged = true
			s.User.LastLogin = time.Now().Unix()
			s.User.Save()
			fmt.Fprintf(s.Conn, "230-Welcome to GoFTPd, %s!\r\n", s.User.Name)
			fmt.Fprintf(s.Conn, "230-Tagline: %s\r\n", s.User.Tagline)

			s.showGlobalStats("230", false)
			fmt.Fprintf(s.Conn, "230 User logged in.\r\n")

		} else {
			fmt.Fprintf(s.Conn, "530 Login incorrect.\r\n")
		}

	case "SYST":
		fmt.Fprintf(s.Conn, "215 UNIX Type: L8\r\n")

	case "TYPE":
		fmt.Fprintf(s.Conn, "200 Type set to I.\r\n")

	case "REST":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		fmt.Fprintf(s.Conn, "350 REST position set.\r\n")

	case "PWD":
		fmt.Fprintf(s.Conn, "257 \"%s\" is current directory.\r\n", s.CurrentDir)

	case "CWD":
		target := "/"
		if len(args) > 0 {
			target = args[0]
		}
		if !strings.HasPrefix(target, "/") {
			target = path.Join(s.CurrentDir, target)
		}
		s.CurrentDir = path.Clean(target)
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				for _, e := range bridge.ListDir(path.Dir(s.CurrentDir)) {
					if e.Name == path.Base(s.CurrentDir) && e.IsSymlink && e.LinkTarget != "" {
						s.CurrentDir = path.Clean(e.LinkTarget)
						break
					}
				}
			}
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				if s.Config.ShowDiz != nil {
					for fileName, permission := range s.Config.ShowDiz {
						if fileName == ".message" {
							continue
						}
						if permission == "*" || s.User.HasFlag(permission) {
							filePath := path.Join(s.CurrentDir, fileName)
							if content, err := bridge.ReadFile(filePath); err == nil && len(content) > 0 {
								text := strings.ReplaceAll(string(content), "\r\n", "\n")
								for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
									fmt.Fprintf(s.Conn, "250-%s\r\n", line)
								}
							}
						}
					}
				}

				users, groups, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)

				if s.Config.Debug {
					log.Printf("[RACESTATS] dir=%s users=%d groups=%d totalBytes=%d present=%d total=%d",
						s.CurrentDir, len(users), len(groups), totalBytes, present, total)
				}

				if present > 0 || total > 0 || len(users) > 0 || len(groups) > 0 || totalBytes > 0 {
					var builder strings.Builder
					RenderRaceStats(
						&builder,
						users,
						groups,
						totalBytes,
						present,
						total,
						s.Config.Version,
					)

					for _, line := range strings.Split(strings.TrimRight(builder.String(), "\r\n"), "\n") {
						fmt.Fprintf(s.Conn, "250-%s\r\n", line)
					}
				}
			}
		}

		s.showGlobalStats("250", false)
		fmt.Fprintf(s.Conn, "250 Directory changed to %s\r\n", s.CurrentDir)

	case "CDUP":
		s.CurrentDir = path.Clean(path.Join(s.CurrentDir, ".."))
		fmt.Fprintf(s.Conn, "250 Directory changed to %s\r\n", s.CurrentDir)

	case "MKD":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}

		requestedPath := args[0]
		var targetPath string
		if path.IsAbs(requestedPath) {
			targetPath = path.Clean(requestedPath)
		} else {
			targetPath = path.Join("/", s.CurrentDir, requestedPath)
		}

		if !path.IsAbs(targetPath) {
			targetPath = "/" + targetPath
		}
		targetPath = path.Clean(targetPath)

		aclPath := path.Join(s.Config.ACLBasePath, targetPath)
		if !s.ACLEngine.CanPerform(s.User, "MKD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}

		dirName := path.Base(targetPath)

		// Decide whether the new dir participates in dupe-checking. Skip:
		//  - section dirs (parent = root) — e.g. /TV-1080P, /X265, /MP3
		//  - known scene subfolders that exist inside many releases
		parent := path.Dir(targetPath)
		isSectionDir := parent == "/" || parent == "."
		isSubFolder := isSceneSubfolder(dirName)
		dupeEligible := !isSectionDir && !isSubFolder

		if dupeEligible && s.DupeChecker != nil {
			if dc, ok := s.DupeChecker.(interface{ IsDupe(string) (bool, error) }); ok {
				if isDupe, err := dc.IsDupe(dirName); err == nil && isDupe {
					fmt.Fprintf(s.Conn, "550 %s: directory already exists in dupe database.\r\n", dirName)
					return false
				}
			}
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				bridge.MakeDir(targetPath, s.User.Name, s.User.PrimaryGroup)
			}
		}

		if dupeEligible && s.DupeChecker != nil {
			if dc, ok := s.DupeChecker.(interface {
				AddDupe(string, string, string, int, int64) error
			}); ok {
				dc.AddDupe(dirName, s.User.PrimaryGroup, s.User.Name, 0, 0)
			}
		}

		s.emitEvent(EventMKDir, targetPath, dirName, 0, 0, nil)

		fmt.Fprintf(s.Conn, "257 \"%s\" created\r\n", targetPath)

	case "RMD":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, args[0])
		if !s.ACLEngine.CanPerform(s.User, "RMD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				dirPath := path.Join(s.CurrentDir, args[0])
				bridge.DeleteFile(dirPath)
			}
		}
		s.emitEvent(EventRMDir, path.Join(s.CurrentDir, args[0]), args[0], 0, 0, nil)
		fmt.Fprintf(s.Conn, "250 Directory removed.\r\n")

	case "SIZE":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, args[0])
				size := bridge.GetFileSize(filePath)
				if size >= 0 {
					fmt.Fprintf(s.Conn, "213 %d\r\n", size)
				} else {
					fmt.Fprintf(s.Conn, "550 File not found.\r\n")
				}
			}
		}

	case "MDTM":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				entries := bridge.ListDir(s.CurrentDir)
				found := false
				for _, e := range entries {
					if e.Name == args[0] {
						fmt.Fprintf(s.Conn, "213 %s\r\n", time.Unix(e.ModTime, 0).Format("20060102150405"))
						found = true
						break
					}
				}
				if !found {
					fmt.Fprintf(s.Conn, "550 File not found.\r\n")
				}
			}
		}

	case "DELE":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, args[0])
		if !s.ACLEngine.CanPerform(s.User, "DELETE", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, args[0])
				bridge.DeleteFile(filePath)
			}
		}
		fmt.Fprintf(s.Conn, "250 File deleted.\r\n")

	case "RNFR":
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		s.RenameFrom = args[0]
		fmt.Fprintf(s.Conn, "350 File exists, ready for destination name.\r\n")

	case "RNTO":
		if len(args) == 0 || s.RenameFrom == "" {
			fmt.Fprintf(s.Conn, "503 Bad sequence of commands.\r\n")
			return false
		}
		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, args[0])
		if !s.ACLEngine.CanPerform(s.User, "RENAME", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				fromPath := path.Join(s.CurrentDir, s.RenameFrom)
				toDir := s.CurrentDir
				toName := args[0]
				bridge.RenameFile(fromPath, toDir, toName)
			}
		}
		fmt.Fprintf(s.Conn, "250 Rename successful.\r\n")
		s.RenameFrom = ""

	case "SITE":
		return s.DispatchSiteCommand(args)

	case "PRET":
		if s.Config.Debug && len(args) > 0 {
			log.Printf("[PRET] Client preparing for %s", args[0])
		}
		if len(args) > 0 {
			s.PretCmd = strings.ToUpper(args[0])
			if len(args) > 1 {
				s.PretArg = args[1]
			}
			fmt.Fprintf(s.Conn, "200 OK, preparing for %s\r\n", args[0])
		} else {
			fmt.Fprintf(s.Conn, "200 OK\r\n")
		}
		return false

	case "ABOR":
		fmt.Fprintf(s.Conn, "226 Abort successful\r\n")
		return false

	case "NOOP":
		fmt.Fprintf(s.Conn, "200 NOOP OK\r\n")
		return false

	case "PASV":
		if s.Config.Debug {
			log.Printf("[PASV] Starting passive mode setup (pret=%s, passthrough=%v)", s.PretCmd, s.Config.Passthrough)
		}

		if s.Config.Passthrough && s.Config.Mode == "master" && s.MasterManager != nil {
			if s.PretCmd == "STOR" || s.PretCmd == "RETR" {
				if bridge, ok := s.MasterManager.(MasterBridge); ok {
					slaveIP, port, xferIdx, slaveName, err := bridge.SlaveListenForPassthrough(s.CurrentDir)
					if err != nil {
						log.Printf("[PASV] Passthrough slave listen failed: %v", err)
						fmt.Fprintf(s.Conn, "421 No available slave for passthrough.\r\n")
						return false
					}
					s.PassthruSlave = slaveName
					s.PassthruXferIdx = xferIdx
					if s.DataListen != nil {
						s.DataListen.Close()
						s.DataListen = nil
					}
					ip := strings.ReplaceAll(slaveIP, ".", ",")
					response := fmt.Sprintf("227 Entering Passive Mode (%s,%d,%d)\r\n", ip, port/256, port%256)
					if s.Config.Debug {
						log.Printf("[PASV] Passthrough to slave %s: %s (port: %d, xferIdx: %d)", slaveName, strings.TrimSpace(response), port, xferIdx)
					}
					fmt.Fprintf(s.Conn, response)
					return false
				}
			}
		}

		var l net.Listener
		var port int
		var err error
		for p := s.Config.PasvMin; p <= s.Config.PasvMax; p++ {
			l, err = net.Listen("tcp", fmt.Sprintf(":%d", p))
			if err == nil {
				port = p
				break
			}
		}
		if l == nil {
			if s.Config.Debug {
				log.Printf("[PASV] No available ports (tried %d-%d)", s.Config.PasvMin, s.Config.PasvMax)
			}
			fmt.Fprintf(s.Conn, "421 No available passive ports.\r\n")
			return false
		}
		s.DataListen = l
		s.PassthruSlave = nil 
		ip := strings.ReplaceAll(s.Config.PublicIP, ".", ",")
		response := fmt.Sprintf("227 Entering Passive Mode (%s,%d,%d)\r\n", ip, port/256, port%256)
		if s.Config.Debug {
			log.Printf("[PASV] Sending response: %s (port: %d)", strings.TrimSpace(response), port)
		}
		fmt.Fprintf(s.Conn, response)
		return false

	case "PORT":
		if len(args) == 0 {
			return false
		}
		parts := strings.Split(args[0], ",")
		if len(parts) != 6 {
			fmt.Fprintf(s.Conn, "501 Syntax error\r\n")
			return false
		}
		ip := strings.Join(parts[:4], ".")
		p1, _ := strconv.Atoi(parts[4])
		p2, _ := strconv.Atoi(parts[5])
		s.ActiveAddr = fmt.Sprintf("%s:%d", ip, p1*256+p2)
		fmt.Fprintf(s.Conn, "200 PORT command successful.\r\n")

	case "MLST":
		target := s.CurrentDir
		if len(args) > 0 && strings.TrimSpace(args[0]) != "" {
			t := strings.TrimSpace(args[0])
			if strings.HasPrefix(t, "/") {
				target = path.Clean(t)
			} else {
				target = path.Clean(path.Join(s.CurrentDir, t))
			}
		}

		facts := ""
		found := false
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				if target == "/" {
					facts = "Type=dir;Perm=elcmp; /"
					found = true
				} else {
					parent := path.Dir(target)
					name := path.Base(target)
					for _, e := range bridge.ListDir(parent) {
						if e.Name == name {
							ts := time.Unix(e.ModTime, 0).Format("20060102150405")
							var parts []string
							if e.IsSymlink {
								parts = []string{
									fmt.Sprintf("Modify=%s", ts),
									"Perm=el",
									"Type=dir",
								}
							} else if e.IsDir {
								perm := "elcmp"
								if e.Mode == 0555 {
									perm = "el"
								}
								parts = []string{
									fmt.Sprintf("Modify=%s", ts),
									"Perm=" + perm,
									"Type=dir",
								}
							} else {
								parts = []string{
									fmt.Sprintf("Modify=%s", ts),
									"Perm=radfw",
									"Type=file",
									fmt.Sprintf("Size=%d", e.Size),
								}
							}
							facts = strings.Join(parts, ";") + "; " + target
							found = true
							break
						}
					}
				}
			}
		}

		if !found {
			fmt.Fprintf(s.Conn, "550 %s: no such file or directory\r\n", target)
			return false
		}
		fmt.Fprintf(s.Conn, "250- Listing %s\r\n", target)
		fmt.Fprintf(s.Conn, " %s\r\n", facts)
		fmt.Fprintf(s.Conn, "250 End\r\n")

	case "MLSD":
		if s.Config.Debug {
			log.Printf("[MLSD] Client requesting machine list for %s", s.CurrentDir)
		}
		fmt.Fprintf(s.Conn, "150 File status okay; about to open data connection.\r\n")

		raw, err := s.getRawDataConn()
		if err != nil {
			fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
			return false
		}
		dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
		if err != nil {
			raw.Close()
			fmt.Fprintf(s.Conn, "435 Failed TLS negotiation on data channel\r\n")
			return false
		}

		var output strings.Builder

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				entries := bridge.ListDir(s.CurrentDir)

				// Race-stats virtual entry — mirrors the [HV] - ( ... COMPLETE ) - [HV]
				// row that LIST shows. Rendered as Type=dir so it appears at the top
				// of client browsers the same way LIST's drwxr-xr-x row did.
				_, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)
				if total > 0 {
					siteName := s.Config.SiteNameShort
					if siteName == "" {
						siteName = "GoFTPd"
					}
					var statusName string
					if present >= total {
						totalMB := float64(totalBytes) / (1024 * 1024)
						statusName = fmt.Sprintf("[%s] - ( %.0fM %dF - COMPLETE ) - [%s]",
							siteName, totalMB, total, siteName)
					} else {
						pct := (present * 100) / total
						bar := "["
						barWidth := 20
						filled := (present * barWidth) / total
						for i := 0; i < barWidth; i++ {
							if i < filled {
								bar += "#"
							} else {
								bar += ":"
							}
						}
						bar += "]"
						statusName = fmt.Sprintf("%s - %3d%% Complete - [%s]", bar, pct, siteName)
					}
					nowTs := time.Now().Format("20060102150405")
					output.WriteString(fmt.Sprintf("Modify=%s;Perm=el;Type=dir; %s\r\n", nowTs, statusName))
				}

				for _, e := range entries {
					if strings.HasPrefix(e.Name, ".") {
						continue
					}
					aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, e.Name)
					if !s.ACLEngine.CanPerform(s.User, "LIST", aclPath) {
						continue
					}
					ts := time.Unix(e.ModTime, 0).Format("20060102150405")
					var perm string
					var facts []string
					if e.IsSymlink {
						perm = "el"
						facts = []string{
							fmt.Sprintf("Modify=%s", ts),
							"Perm=" + perm,
							"Type=dir",
						}
					} else if e.IsDir {
						perm = "elcmp" // enter, list, create, mkdir, purge
						if e.Mode == 0555 {
							perm = "el"
						}
						facts = []string{
							fmt.Sprintf("Modify=%s", ts),
							"Perm=" + perm,
							"Type=dir",
						}
					} else {
						perm = "radfw" // read, append, delete, rename, write
						facts = []string{
							fmt.Sprintf("Modify=%s", ts),
							"Perm=" + perm,
							"Type=file",
							fmt.Sprintf("Size=%d", e.Size),
						}
					}
					output.WriteString(strings.Join(facts, ";") + "; " + e.Name + "\r\n")
				}
			}
		} else {
			mlsdPath := filepath.Join(s.Config.StoragePath, s.CurrentDir)
			files, err := os.ReadDir(mlsdPath)
			if err != nil {
				if s.Config.Debug {
					log.Printf("[MLSD] ReadDir %s: %v", mlsdPath, err)
				}
			}
			for _, f := range files {
				if strings.HasPrefix(f.Name(), ".") {
					continue
				}
				if !s.Config.ShowSymlinks && f.Type()&fs.ModeSymlink != 0 {
					continue
				}
				fileName := f.Name()
				fullPath := filepath.Join(mlsdPath, fileName)
				isSymlink := f.Type()&fs.ModeSymlink != 0
				var info os.FileInfo
				if isSymlink {
					info, err = os.Lstat(fullPath)
				} else {
					info, err = f.Info()
				}
				if err != nil || info == nil {
					continue
				}
				facts := []string{
					fmt.Sprintf("Modify=%s", info.ModTime().Format("20060102150405")),
					fmt.Sprintf("Perm=%s", getMlsdPerm(info, isSymlink)),
				}
				if isSymlink {
					facts = append(facts, "Type=OS.unix=symlink")
				} else if info.IsDir() {
					facts = append(facts, "Type=dir")
				} else {
					facts = append(facts, "Type=file")
					facts = append(facts, fmt.Sprintf("Size=%d", info.Size()))
				}
				output.WriteString(strings.Join(facts, ";") + "; " + fileName + "\r\n")
			}
		}

		dataConn.Write([]byte(output.String()))
		dataConn.Close()
		fmt.Fprintf(s.Conn, "226 Directory listing complete.\r\n")
		return false

	case "LIST":
		fmt.Fprintf(s.Conn, "150 Opening ASCII mode data connection.\r\n")

		raw, err := s.getRawDataConn()
		if err != nil {
			fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
			return false
		}
		dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
		if err != nil {
			raw.Close()
			fmt.Fprintf(s.Conn, "435 Failed TLS negotiation on data channel\r\n")
			return false
		}

		var output strings.Builder

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				entries := bridge.ListDir(s.CurrentDir)
				now := time.Now().Format("Jan _2 15:04")
				siteName := s.Config.SiteNameShort
				if siteName == "" {
					siteName = "GoFTPd"
				}

				_, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)

				if s.Config.Debug {
					log.Printf("[LIST/RACESTATS] dir=%s totalBytes=%d present=%d total=%d",
						s.CurrentDir, totalBytes, present, total)
				}

				existingFiles := make(map[string]bool)
				for _, e := range entries {
					existingFiles[e.Name] = true
				}

				if total > 0 {
					pct := (present * 100) / total
					totalMB := float64(totalBytes) / (1024 * 1024)

					var statusName string
					if present >= total {
						statusName = fmt.Sprintf("[%s] - ( %.0fM %dF - COMPLETE ) - [%s]",
							siteName, totalMB, total, siteName)
					} else {
						bar := "["
						barWidth := 20
						filled := (present * barWidth) / total
						for i := 0; i < barWidth; i++ {
							if i < filled {
								bar += "#"
							} else {
								bar += ":"
							}
						}
						bar += "]"
						statusName = fmt.Sprintf("%s - %3d%% Complete - [%s]", bar, pct, siteName)
					}
					output.WriteString(fmt.Sprintf("drwxr-xr-x   1 %-8s %-8s %10s %s %s\r\n",
						"GoFTPd", "GoFTPd", "4096", now, statusName))
				}

				for _, e := range entries {
					if strings.HasPrefix(e.Name, ".") {
						continue
					}
					if strings.HasSuffix(e.Name, "-missing") || strings.HasSuffix(e.Name, "-MISSING") {
						continue
					}
					if strings.HasPrefix(e.Name, "[incomplete]") {
						continue
					}
					if strings.HasPrefix(e.Name, "[#") || strings.HasPrefix(e.Name, "[:") {
						continue
					}
					if strings.Contains(e.Name, "COMPLETE") && strings.Contains(e.Name, "[") {
						continue
					}

					aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, e.Name)
					if !s.ACLEngine.CanPerform(s.User, "LIST", aclPath) {
						continue
					}

					mode := ftpListMode(e)
					size := fmt.Sprintf("%d", e.Size)
					name := e.Name
					if e.IsSymlink {
						size = "0"
						name = fmt.Sprintf("%s -> %s", e.Name, e.LinkTarget)
					} else if e.IsDir {
						size = "4096"
					}
					ts := time.Unix(e.ModTime, 0).Format("Jan _2 15:04")
					owner := "GoFTPd"
					group := "GoFTPd"
					output.WriteString(fmt.Sprintf("%s   1 %-8s %-8s %10s %s %s\r\n",
						mode, owner, group, size, ts, name))
				}

				if total > 0 && present < total {
					sfvMeta := bridge.GetSFVData(s.CurrentDir)
					if sfvMeta != nil {
						for fileName := range sfvMeta {
							if !existingFiles[fileName] {
								output.WriteString(fmt.Sprintf("-rw-r--r--   1 %-8s %-8s %10s %s %s-MISSING\r\n",
									"GoFTPd", "GoFTPd", "0", now, fileName))
							}
						}
					}
				}
			}
		} else {
			// FALLBACK: Standalone mode directory listing for cbftp
			listPath := filepath.Join(s.Config.StoragePath, s.CurrentDir)
			files, err := os.ReadDir(listPath)
			if err == nil {
				
				for _, f := range files {
					if strings.HasPrefix(f.Name(), ".") {
						continue
					}
					if !s.Config.ShowSymlinks && f.Type()&fs.ModeSymlink != 0 {
						continue
					}
					info, err := f.Info()
					if err != nil {
						continue
					}
					mode := "-rw-r--r--"
					size := fmt.Sprintf("%d", info.Size())
					if info.IsDir() {
						mode = "drwxr-xr-x"
						size = "4096"
					} else if f.Type()&fs.ModeSymlink != 0 {
						mode = "lrwxrwxrwx"
					}
					ts := info.ModTime().Format("Jan _2 15:04")
					output.WriteString(fmt.Sprintf("%s   1 %-8s %-8s %10s %s %s\r\n",
						mode, "GoFTPd", "GoFTPd", size, ts, f.Name()))
				}
			}
		}

		dataConn.Write([]byte(output.String()))
		dataConn.Close()

		// Only show stats in master mode so we don't crash standalone
		if s.Config.Mode == "master" && s.MasterManager != nil {
			s.showGlobalStats("226", false)
		}
		
		fmt.Fprintf(s.Conn, "226 Directory listing complete.\r\n")
		return false

	case "STOR":
		if len(args) == 0 {
			return false
		}

		isTLSExempt := false
		for _, exemptUser := range s.Config.TLSExemptUsers {
			if exemptUser == s.User.Name {
				isTLSExempt = true
				break
			}
		}
		if s.Config.RequireTLSData && !isTLSExempt && !s.DataTLS {
			fmt.Fprintf(s.Conn, "550 TLS required for data transfers.\r\n")
			return false
		}

		fileName := args[0]
		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, fileName)
		if !s.ACLEngine.CanPerform(s.User, "UPLOAD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Cannot upload here.\r\n")
			return false
		}

		if s.Config.XdupeEnabled {
			fileExists := false
			if s.Config.Mode == "master" && s.MasterManager != nil {
				if bridge, ok := s.MasterManager.(MasterBridge); ok {
					filePath := path.Join(s.CurrentDir, fileName)
					fileExists = bridge.FileExists(filePath)
				}
			}
			if fileExists {
				fmt.Fprintf(s.Conn, "553 %s: file already exists (X-DUPE)\r\n", fileName)
				return false
			}
		}

		if s.Config.Passthrough && s.Config.Mode == "master" && s.MasterManager != nil && s.ActiveAddr != "" && s.PassthruSlave == nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, fileName)
				portAddr := s.ActiveAddr
				s.ActiveAddr = ""

				log.Printf("[Passthrough] PORT STOR %s → slave connects to %s", filePath, portAddr)
				fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")

				fileSize, checksum, xferMs, err := bridge.SlaveConnectAndReceive(filePath, portAddr, s.User.Name, s.User.PrimaryGroup)
				_ = xferMs

				if err != nil {
					log.Printf("[Passthrough] PORT upload failed: %v", err)
					fmt.Fprintf(s.Conn, "550 Upload failed: %v\r\n", err)
					return false
				}

				if fileSize == 0 {
					bridge.DeleteFile(filePath)
					log.Printf("[MASTER-ZS] Deleted 0-byte file: %s", filePath)
					fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
					return false
				}

				if checksum > 0 && !strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					sfvEntries := bridge.GetSFVData(s.CurrentDir)
					if sfvEntries != nil {
						if expectedCRC, exists := sfvEntries[fileName]; exists {
							if expectedCRC != checksum {
								bridge.DeleteFile(filePath)
								log.Printf("[MASTER-ZS] CRC mismatch for %s: got %08X, expected %08X — deleted",
									fileName, checksum, expectedCRC)
								fmt.Fprintf(s.Conn, "226- checksum mismatch: SLAVE: %08X SFV: %08X\r\n", checksum, expectedCRC)
								fmt.Fprintf(s.Conn, "226 Checksum mismatch, deleting file\r\n")
								return false
							}
							if s.Config.Debug {
								log.Printf("[MASTER-ZS] CRC match for %s: %08X", fileName, checksum)
							}
						}
					}
				}

				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries, err := bridge.GetSFVInfo(filePath); err == nil {
						log.Printf("[MASTER-ZS] Parsed SFV %s: %d entries", fileName, len(sfvEntries))
						bridge.CacheSFV(s.CurrentDir, fileName, sfvEntries)
					}
				}

				if fileSize > 0 {
					s.User.UpdateStats(fileSize, true)
				}
				speedMB := 0.0
				if xferMs > 0 {
					speedMB = (float64(fileSize) / 1024.0 / 1024.0) / (float64(xferMs) / 1000.0)
				}
				data := map[string]string{}
				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
						data["t_filecount"] = fmt.Sprintf("%d", len(sfvEntries))
					}
				}
				if regexp.MustCompile(`(?i)\.(rar|r\d\d)$`).MatchString(fileName) {
					data["t_mbytes"] = mbString(fileSize)
					if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
						users, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)
						if total > 0 {
							data["relname"] = path.Base(s.CurrentDir)
							data["t_files"] = fmt.Sprintf("%d", total)
							data["t_present"] = fmt.Sprintf("%d", present)
							data["t_totalmb"] = fmt.Sprintf("%.1f", float64(totalBytes)/1024.0/1024.0)
							estBytes := fileSize * int64(total)
							data["t_mbytes"] = fmt.Sprintf("%.0fMB", float64(estBytes)/1024.0/1024.0)
							if len(users) > 0 {
								leader := users[0]
								for _, u := range users {
									if u.Files > leader.Files {
										leader = u
									}
								}
								data["leader_name"] = leader.Name
								data["leader_group"] = leader.Group
								data["leader_files"] = fmt.Sprintf("%d", leader.Files)
								data["leader_mb"] = fmt.Sprintf("%.1f", float64(leader.Bytes)/1024.0/1024.0)
								data["leader_pct"] = fmt.Sprintf("%d", leader.Percent)
								data["leader_speed"] = fmt.Sprintf("%.2fMB/s", leader.Speed/1024.0/1024.0)
							}
						}
					}
				}
				s.emitEvent(EventUpload, filePath, fileName, fileSize, speedMB, data)
				if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
					users, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)
					if total > 0 && present >= total {
						// Race complete: fire COMPLETE/STATS sequence in a
						// goroutine so the client gets 226 immediately. The
						// FIFO writes + plugin dispatches were stacking up on
						// the connection's hot path and delaying the final
						// transfer ack by the time it took to do all that work.
						go emitRaceEnd(s, users, totalBytes, total, xferMs)
					}
				}

				fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
				return false
			}
		}

		raw, err := s.getRawDataConn()
		if err != nil {
			if s.PassthruSlave != nil && s.Config.Passthrough {
			} else {
				fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
				return false
			}
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, fileName)

				var fileSize int64
				var checksum uint32
				var xferMs int64

				if s.PassthruSlave != nil && s.Config.Passthrough {
					slaveName := s.PassthruSlave.(string)
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")
					log.Printf("[Passthrough] STOR %s via slave %s (xferIdx=%d)", filePath, slaveName, s.PassthruXferIdx)

					fileSize, checksum, xferMs, err = bridge.SlaveReceivePassthrough(filePath, s.PassthruXferIdx, slaveName, s.User.Name, s.User.PrimaryGroup)
					s.PassthruSlave = nil
					s.PretCmd = ""

					if err != nil {
						log.Printf("[Passthrough] Upload failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Upload failed: %v\r\n", err)
						return false
					}
				} else {
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")
					dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
					if err != nil {
						raw.Close()
						return false
					}

					start := time.Now()
					fileSize, checksum, err = bridge.UploadFile(filePath, dataConn, s.User.Name, s.User.PrimaryGroup)
					xferMs = time.Since(start).Milliseconds()
					dataConn.Close()

					if err != nil {
						log.Printf("[MASTER] Upload failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Upload failed: %v\r\n", err)
						return false
					}
				}

				if fileSize == 0 {
					bridge.DeleteFile(filePath)
					log.Printf("[MASTER-ZS] Deleted 0-byte file: %s", filePath)
					fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
					return false
				}

				if checksum > 0 && !strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					sfvEntries := bridge.GetSFVData(s.CurrentDir)
					if sfvEntries != nil {
						if expectedCRC, exists := sfvEntries[fileName]; exists {
							if expectedCRC != checksum {
								bridge.DeleteFile(filePath)
								log.Printf("[MASTER-ZS] CRC mismatch for %s: got %08X, expected %08X — deleted",
									fileName, checksum, expectedCRC)
								fmt.Fprintf(s.Conn, "226- checksum mismatch: SLAVE: %08X SFV: %08X\r\n", checksum, expectedCRC)
								fmt.Fprintf(s.Conn, "226 Checksum mismatch, deleting file\r\n")
								return false
							}
							if s.Config.Debug {
								log.Printf("[MASTER-ZS] CRC match for %s: %08X", fileName, checksum)
							}
						}
					}
				}

				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries, err := bridge.GetSFVInfo(filePath); err == nil {
						log.Printf("[MASTER-ZS] Parsed SFV %s: %d entries", fileName, len(sfvEntries))
						bridge.CacheSFV(s.CurrentDir, fileName, sfvEntries)
					}
				}

				if fileSize > 0 {
					s.User.UpdateStats(fileSize, true)
				}
				speedMB := 0.0
				if xferMs > 0 {
					speedMB = (float64(fileSize) / 1024.0 / 1024.0) / (float64(xferMs) / 1000.0)
				}
				data := map[string]string{}
				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
						data["t_filecount"] = fmt.Sprintf("%d", len(sfvEntries))
					}
				}
				if regexp.MustCompile(`(?i)\.(rar|r\d\d)$`).MatchString(fileName) {
					data["t_mbytes"] = mbString(fileSize)
					if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
						users, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)
						if total > 0 {
							data["relname"] = path.Base(s.CurrentDir)
							data["t_files"] = fmt.Sprintf("%d", total)
							data["t_present"] = fmt.Sprintf("%d", present)
							data["t_totalmb"] = fmt.Sprintf("%.1f", float64(totalBytes)/1024.0/1024.0)
							estBytes := fileSize * int64(total)
							data["t_mbytes"] = fmt.Sprintf("%.0fMB", float64(estBytes)/1024.0/1024.0)
							if len(users) > 0 {
								leader := users[0]
								for _, u := range users {
									if u.Files > leader.Files {
										leader = u
									}
								}
								data["leader_name"] = leader.Name
								data["leader_group"] = leader.Group
								data["leader_files"] = fmt.Sprintf("%d", leader.Files)
								data["leader_mb"] = fmt.Sprintf("%.1f", float64(leader.Bytes)/1024.0/1024.0)
								data["leader_pct"] = fmt.Sprintf("%d", leader.Percent)
								data["leader_speed"] = fmt.Sprintf("%.2fMB/s", leader.Speed/1024.0/1024.0)
							}
						}
					}
				}
				s.emitEvent(EventUpload, filePath, fileName, fileSize, speedMB, data)
				if sfvEntries := bridge.GetSFVData(s.CurrentDir); sfvEntries != nil {
					users, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)
					if total > 0 && present >= total {
						// Async — see explanation at the other emitRaceEnd call.
						go emitRaceEnd(s, users, totalBytes, total, xferMs)
					}
				}

				fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
			} else {
				fmt.Fprintf(s.Conn, "550 Master not initialized\r\n")
				if raw != nil {
					raw.Close()
				}
			}
			return false
		}

		if raw != nil {
			raw.Close()
		}
		return false

	case "RETR":
		if len(args) == 0 {
			return false
		}

		isTLSExempt := false
		for _, exemptUser := range s.Config.TLSExemptUsers {
			if exemptUser == s.User.Name {
				isTLSExempt = true
				break
			}
		}
		if s.Config.RequireTLSData && !isTLSExempt && !s.DataTLS {
			fmt.Fprintf(s.Conn, "550 TLS required for data transfers.\r\n")
			return false
		}

		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, args[0])
		if !s.ACLEngine.CanPerform(s.User, "DOWNLOAD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied.\r\n")
			return false
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, args[0])
				fileSize := bridge.GetFileSize(filePath)
				if fileSize < 0 {
					fmt.Fprintf(s.Conn, "550 File not found on any slave.\r\n")
					return false
				}

				if s.PassthruSlave != nil && s.Config.Passthrough {
					slaveName := s.PassthruSlave.(string)
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection for %s (%d bytes).\r\n", args[0], fileSize)
					log.Printf("[Passthrough] RETR %s via slave %s (xferIdx=%d)", filePath, slaveName, s.PassthruXferIdx)

					err := bridge.SlaveSendPassthrough(filePath, s.PassthruXferIdx, slaveName)
					s.PassthruSlave = nil
					s.PretCmd = ""

					if err != nil {
						log.Printf("[Passthrough] Download failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Download failed: %v\r\n", err)
					} else {
						fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
						if fileSize > 0 {
							s.User.UpdateStats(fileSize, false)
						}
						s.emitEvent(EventDownload, filePath, args[0], fileSize, 0, nil)
						s.emitEvent(EventDownload, filePath, args[0], fileSize, 0, nil)
					}
				} else {
					raw, err := s.getRawDataConn()
					if err != nil {
						fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
						return false
					}
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection for %s (%d bytes).\r\n", args[0], fileSize)
					dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
					if err != nil {
						raw.Close()
						return false
					}
					err = bridge.DownloadFile(filePath, dataConn)
					dataConn.Close()
					if err != nil {
						log.Printf("[MASTER] Download failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Download failed: %v\r\n", err)
					} else {
						fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
						if fileSize > 0 {
							s.User.UpdateStats(fileSize, false)
						}
					}
				}
			} else {
				fmt.Fprintf(s.Conn, "550 Master not initialized\r\n")
			}
			return false
		}

	case "STAT":
		// STAT with no args = server status. STAT <path> = listing on control
		// channel (no data connection). cbftp uses STAT -l at login as a
		// cheap way to probe the server without opening a data conn.
		if len(args) == 0 {
			fmt.Fprintf(s.Conn, "211- %s server status:\r\n", s.Config.SiteNameShort)
			fmt.Fprintf(s.Conn, " Connected from %s\r\n", s.Conn.RemoteAddr())
			fmt.Fprintf(s.Conn, " Logged in as %s\r\n", s.User.Name)
			fmt.Fprintf(s.Conn, " TYPE: %s, STRU: F, MODE: S\r\n", "BINARY")
			fmt.Fprintf(s.Conn, "211 End of status.\r\n")
			return false
		}

		// STAT with args — if it's a flag like "-l" or "-la", treat as listing
		// of current dir. If it's a path, list that path.
		target := s.CurrentDir
		arg := strings.TrimSpace(args[0])
		if arg != "" && !strings.HasPrefix(arg, "-") {
			if strings.HasPrefix(arg, "/") {
				target = path.Clean(arg)
			} else {
				target = path.Clean(path.Join(s.CurrentDir, arg))
			}
		}

		fmt.Fprintf(s.Conn, "213- Status of %s:\r\n", target)
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				entries := bridge.ListDir(target)
				for _, e := range entries {
					if strings.HasPrefix(e.Name, ".") {
						continue
					}
					mode := "-rw-r--r--"
					size := fmt.Sprintf("%d", e.Size)
					if e.IsDir {
						mode = "drwxr-xr-x"
						size = "4096"
					}
					ts := time.Unix(e.ModTime, 0).Format("Jan _2 15:04")
					fmt.Fprintf(s.Conn, " %s   1 %-8s %-8s %10s %s %s\r\n",
						mode, "GoFTPd", "GoFTPd", size, ts, e.Name)
				}
			}
		}
		fmt.Fprintf(s.Conn, "213 End of status.\r\n")
		return false

	case "QUIT":
		fmt.Fprintf(s.Conn, "221 Goodbye.\r\n")
		return true

	default:
		fmt.Fprintf(s.Conn, "502 Command not implemented.\r\n")
	}
	return false
}

func (s *Session) showGlobalStats(code string, final bool) {
	var stat syscall.Statfs_t
	wd, _ := os.Getwd()
	if err := syscall.Statfs(s.Config.StoragePath, &stat); err != nil {
		_ = syscall.Statfs(wd, &stat)
	}

	freeSpaceMB := (stat.Bavail * uint64(stat.Bsize)) / 1024 / 1024
	siteSpeedMiB := 0.0

	ulGiB := 0.0
	dlGiB := 0.0
	creditsGiB := 0.0
	ratioStr := "UL&DL: Unlimited"

	if s.User != nil {
		ulGiB = float64(s.User.AllUp.Bytes) / (1024 * 1024 * 1024)
		dlGiB = float64(s.User.AllDn.Bytes) / (1024 * 1024 * 1024)
		creditsGiB = float64(s.User.Credits) / (1024 * 1024 * 1024)
		if s.User.Ratio > 0 {
			ratioStr = fmt.Sprintf("1:%d", s.User.Ratio)
		}
	}

	fmt.Fprintf(s.Conn, "%s- [Ul: %.1fGiB] [Dl: %.1fGiB] [Speed: %.2fMiB/s] [Free: %dMB]\r\n",
		code, ulGiB, dlGiB, siteSpeedMiB, freeSpaceMB)

	if final {
		fmt.Fprintf(s.Conn, "%s  [Section: DEFAULT] [Credits: %.1fGiB] [Ratio: %s]\r\n",
			code, creditsGiB, ratioStr)
	} else {
		fmt.Fprintf(s.Conn, "%s- [Section: DEFAULT] [Credits: %.1fGiB] [Ratio: %s]\r\n",
			code, creditsGiB, ratioStr)
	}
}

func mbString(size int64) string { return fmt.Sprintf("%.0fMB", float64(size)/1024.0/1024.0) }

func ftpListMode(e MasterFileEntry) string {
	switch {
	case e.IsSymlink:
		return "lrwxrwxrwx"
	case e.IsDir:
		if e.Mode == 0555 {
			return "dr-xr-xr-x"
		}
		return "drwxr-xr-x"
	default:
		if e.Mode == 0444 {
			return "-r--r--r--"
		}
		return "-rw-r--r--"
	}
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
