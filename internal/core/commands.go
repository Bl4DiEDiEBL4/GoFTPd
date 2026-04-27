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
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	"goftpd/internal/timeutil"
	"goftpd/internal/user"
	"goftpd/internal/zipscript"
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
				targetPath := s.CurrentDir
				if strings.TrimSpace(s.PretArg) != "" {
					targetPath = path.Join(s.CurrentDir, s.PretArg)
				}

				var slaveIP string
				var port int
				var xferIdx int32
				var slaveName string
				var err error
				if s.PretCmd == "RETR" {
					slaveIP, port, xferIdx, slaveName, err = bridge.SlaveListenForDownloadPassthrough(targetPath, s.DataTLS, true)
				} else {
					slaveIP, port, xferIdx, slaveName, err = bridge.SlaveListenForPassthrough(targetPath, s.DataTLS, true)
				}
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
		s.PendingUser = args[0]
		s.PendingReason = ""
		s.User = nil
		u, err := user.LoadUser(args[0], s.GroupMap)
		if err != nil {
			if _, statErr := os.Stat(deletedUserPath(args[0])); statErr == nil {
				s.PendingReason = "user_deleted"
			} else {
				s.PendingReason = "unknown_user"
			}
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
						s.emitLoginFailure(s.User.Name, remoteIP, "ip_restricted")
						fmt.Fprintf(s.Conn, "530 Login not allowed from this IP.\r\n")
						return false
					}
				}
			}

			if s.User.IsExpired() {
				s.emitLoginFailure(s.User.Name, remoteIP, "account_expired")
				fmt.Fprintf(s.Conn, "530 Account expired.\r\n")
				return false
			}
			if s.User.IsDisabled() {
				s.emitLoginFailure(s.User.Name, remoteIP, "account_disabled")
				fmt.Fprintf(s.Conn, "530 Account disabled.\r\n")
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
				s.emitLoginFailure(s.User.Name, remoteIP, "bad_password")
				fmt.Fprintf(s.Conn, "530 Login incorrect.\r\n")
				return false
			}

			if !s.User.IPAllowed(remoteIP) {
				s.emitLoginFailure(s.User.Name, remoteIP, "ip_not_allowed")
				fmt.Fprintf(s.Conn, "530 IP not allowed.\r\n")
				return false
			}

			if s.Config.PluginManager != nil {
				if err := s.Config.PluginManager.ValidateLogin(s.User, remoteIP); err != nil {
					if s.Config.Debug {
						log.Printf("[PASS] User %s rejected by plugin login policy: %v", s.User.Name, err)
					}
					s.emitLoginFailure(s.User.Name, remoteIP, "plugin_rejected")
					fmt.Fprintf(s.Conn, "530 %s.\r\n", err.Error())
					return false
				}
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
				s.emitLoginFailure(s.User.Name, remoteIP, "tls_required")
				fmt.Fprintf(s.Conn, "530 TLS required.\r\n")
				return false
			}

			s.IsLogged = true
			s.PendingUser = ""
			s.PendingReason = ""
			if strings.TrimSpace(s.User.CurrentDir) != "" {
				s.CurrentDir = path.Clean(s.User.CurrentDir)
			}
			s.User.LastLogin = time.Now().Unix()
			s.User.Save()
			fmt.Fprintf(s.Conn, "230-Welcome to GoFTPd, %s!\r\n", s.User.Name)
			fmt.Fprintf(s.Conn, "230-Tagline: %s\r\n", s.User.Tagline)

			s.showGlobalStats("230", false)
			fmt.Fprintf(s.Conn, "230 User logged in.\r\n")

		} else {
			remoteIP, _, _ := net.SplitHostPort(s.Conn.RemoteAddr().String())
			reason := s.PendingReason
			if reason == "" {
				reason = "unknown_user"
			}
			s.emitLoginFailure(s.PendingUser, remoteIP, reason)
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
		offset, err := strconv.ParseInt(strings.TrimSpace(args[0]), 10, 64)
		if err != nil || offset < 0 {
			fmt.Fprintf(s.Conn, "501 Invalid REST offset\r\n")
			return false
		}
		if offset > 0 && s.Config.Zipscript.Enabled && !s.Config.Zipscript.SFV.AllowResume {
			fmt.Fprintf(s.Conn, "504 Resume disabled by zipscript.\r\n")
			return false
		}
		s.RestOffset = offset
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
				parent := path.Dir(s.CurrentDir)
				name := path.Base(s.CurrentDir)
				for _, e := range bridge.ListDir(parent) {
					if e.Name == name && e.IsSymlink && e.LinkTarget != "" {
						s.CurrentDir = path.Clean(e.LinkTarget)
						break
					}
				}
				if resolved := resolveIncompleteMarkerTarget(bridge, s.Config, activeIncompleteIndicator(s.Config), parent, name); resolved != "" {
					s.CurrentDir = resolved
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
				users = trimRaceUsers(s.Config, users)
				groups = trimRaceGroups(s.Config, groups)

				if s.Config.Debug {
					log.Printf("[RACESTATS] dir=%s users=%d groups=%d totalBytes=%d present=%d total=%d",
						s.CurrentDir, len(users), len(groups), totalBytes, present, total)
				}

				if HasRaceStats(users, groups, totalBytes, present, total) {
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
				} else if s.Config.ShowCWDBanner {
					var builder strings.Builder
					RenderRaceHeader(&builder, s.Config.Version)
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
		skipDupeCheck := s.ACLEngine != nil && s.ACLEngine.CanPerformRuleOnly(s.User, "NODUPECHECK", aclPath)
		dupeEligible := !isSectionDir && !isSubFolder && !skipDupeCheck

		if s.Config.PluginManager != nil {
			if err := s.Config.PluginManager.ValidateMKDir(s.User, targetPath); err != nil {
				fmt.Fprintf(s.Conn, "550 %v\r\n", err)
				return false
			}
		}

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
						fmt.Fprintf(s.Conn, "213 %s\r\n", timeutil.FTPMachineUnix(e.ModTime))
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
		filePath := path.Join(s.CurrentDir, args[0])
		if !s.canDeletePath(filePath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
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
		fromPath := path.Join(s.CurrentDir, s.RenameFrom)
		toPath := path.Join(s.CurrentDir, args[0])
		if !s.canRenamePath(fromPath, toPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
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
					targetPath := s.CurrentDir
					if strings.TrimSpace(s.PretArg) != "" {
						targetPath = path.Join(s.CurrentDir, s.PretArg)
					}

					var slaveIP string
					var port int
					var xferIdx int32
					var slaveName string
					var err error
					if s.PretCmd == "RETR" {
						slaveIP, port, xferIdx, slaveName, err = bridge.SlaveListenForDownloadPassthrough(targetPath, s.DataTLS, false)
					} else {
						slaveIP, port, xferIdx, slaveName, err = bridge.SlaveListenForPassthrough(targetPath, s.DataTLS, false)
					}
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
		s.PassthruXferIdx = 0
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
							ts := timeutil.FTPMachineUnix(e.ModTime)
							var parts []string
							if e.IsSymlink {
								parts = []string{
									fmt.Sprintf("Modify=%s", ts),
									"Perm=el",
									"Type=" + mlsdSymlinkType(e),
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
				siteName := s.Config.SiteNameShort
				if siteName == "" {
					siteName = "GoFTPd"
				}
				if statusName := dirRaceStatusName(bridge, s.Config, s.CurrentDir, siteName); strings.TrimSpace(statusName) != "" {
					nowTs := timeutil.FTPMachine(time.Now())
					output.WriteString(fmt.Sprintf("Modify=%s;Perm=el;Type=dir; %s\r\n", nowTs, statusName))
				}

				for _, marker := range incompleteMarkerEntries(bridge, s.Config, activeIncompleteIndicator(s.Config), s.CurrentDir, entries) {
					ts := timeutil.FTPMachineUnix(marker.ModTime)
					output.WriteString(fmt.Sprintf("Modify=%s;Perm=el;Type=%s; %s\r\n",
						ts, mlsdSymlinkType(marker), marker.Name))
				}

				for _, e := range entries {
					if strings.HasPrefix(e.Name, ".") {
						continue
					}
					if isIncompleteMarkerName(activeIncompleteIndicator(s.Config), e.Name) {
						continue
					}
					aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, e.Name)
					if !s.ACLEngine.CanPerform(s.User, "LIST", aclPath) {
						continue
					}
					ts := timeutil.FTPMachineUnix(e.ModTime)
					var perm string
					var facts []string
					if e.IsSymlink {
						perm = "el"
						facts = []string{
							fmt.Sprintf("Modify=%s", ts),
							"Perm=" + perm,
							"Type=" + mlsdSymlinkType(e),
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
					fmt.Sprintf("Modify=%s", timeutil.FTPMachine(info.ModTime())),
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
				now := timeutil.Now().Format("Jan _2 15:04")
				siteName := s.Config.SiteNameShort
				if siteName == "" {
					siteName = "GoFTPd"
				}

				totalBytes, present, total := dirRaceProgress(bridge, s.Config, s.CurrentDir)

				if s.Config.Debug {
					log.Printf("[LIST/RACESTATS] dir=%s totalBytes=%d present=%d total=%d",
						s.CurrentDir, totalBytes, present, total)
				}

				existingFiles := make(map[string]bool)
				for _, e := range entries {
					existingFiles[e.Name] = true
				}

				if statusName := dirRaceStatusName(bridge, s.Config, s.CurrentDir, siteName); strings.TrimSpace(statusName) != "" {
					output.WriteString(fmt.Sprintf("drwxr-xr-x   1 %-8s %-8s %10s %s %s\r\n",
						"GoFTPd", "GoFTPd", "4096", now, statusName))
				}

				for _, marker := range incompleteMarkerEntries(bridge, s.Config, activeIncompleteIndicator(s.Config), s.CurrentDir, entries) {
					ts := timeutil.Unix(marker.ModTime).Format("Jan _2 15:04")
					name := fmt.Sprintf("%s -> %s", marker.Name, marker.LinkTarget)
					output.WriteString(fmt.Sprintf("%s   1 %-8s %-8s %10s %s %s\r\n",
						ftpListMode(marker), marker.Owner, marker.Group, "0", ts, name))
				}

				for _, e := range entries {
					if strings.HasPrefix(e.Name, ".") {
						continue
					}
					if strings.HasSuffix(e.Name, "-missing") || strings.HasSuffix(e.Name, "-MISSING") {
						continue
					}
					if isIncompleteMarkerName(activeIncompleteIndicator(s.Config), e.Name) {
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
					ts := timeutil.Unix(e.ModTime).Format("Jan _2 15:04")
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
					ts := timeutil.In(info.ModTime()).Format("Jan _2 15:04")
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
		restOffset := s.RestOffset
		s.RestOffset = 0
		var existingNames []string
		uploadDir := s.CurrentDir
		uploadPath := path.Clean(path.Join(s.CurrentDir, fileName))
		if path.IsAbs(strings.TrimSpace(fileName)) {
			uploadPath = path.Clean(fileName)
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				uploadPath = bridge.ResolvePath(uploadPath)
			}
		}
		uploadDir = path.Dir(uploadPath)
		fileName = path.Base(uploadPath)

		aclPath := path.Join(s.Config.ACLBasePath, uploadPath)
		if !s.ACLEngine.CanPerform(s.User, "UPLOAD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Cannot upload here.\r\n")
			return false
		}

		if s.Config.XdupeEnabled {
			fileExists := false
			if s.Config.Mode == "master" && s.MasterManager != nil {
				if bridge, ok := s.MasterManager.(MasterBridge); ok {
					fileExists = bridge.FileExists(uploadPath)
				}
			}
			if fileExists && restOffset == 0 {
				fmt.Fprintf(s.Conn, "553 %s: file already exists (X-DUPE)\r\n", fileName)
				return false
			}
		}
		if restOffset > 0 {
			if s.Config.Mode == "master" && s.MasterManager != nil {
				if bridge, ok := s.MasterManager.(MasterBridge); ok {
					size := bridge.GetFileSize(uploadPath)
					if size < 0 {
						fmt.Fprintf(s.Conn, "550 Resume target not found.\r\n")
						return false
					}
					if restOffset > size {
						fmt.Fprintf(s.Conn, "550 Resume offset beyond end of file.\r\n")
						return false
					}
				}
			}
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				existingNames = zipscriptExistingNames(bridge, uploadDir)
				if shouldBlockZipDIZUpload(s.Config, uploadDir, fileName) {
					fmt.Fprintf(s.Conn, "550 zipscript: upload file_id.diz inside the zip, not as a standalone file\r\n")
					return false
				}
				if err := zipscript.ValidateUpload(s.Config.Zipscript, uploadDir, fileName, existingNames, bridge.GetSFVData(uploadDir)); err != nil {
					fmt.Fprintf(s.Conn, "550 %s\r\n", err)
					return false
				}
			}
		}

		if s.Config.Passthrough && s.Config.Mode == "master" && s.MasterManager != nil && s.ActiveAddr != "" && s.PassthruSlave == nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := uploadPath
				portAddr := s.ActiveAddr
				s.ActiveAddr = ""

				log.Printf("[Passthrough] PORT STOR %s → slave connects to %s", filePath, portAddr)
				fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")
				s.beginTransfer("upload", filePath)
				defer s.endTransfer()

				fileSize, checksum, xferMs, err := bridge.SlaveConnectAndReceive(filePath, portAddr, s.User.Name, s.User.PrimaryGroup, restOffset)
				_ = xferMs

				if err != nil {
					log.Printf("[Passthrough] PORT upload failed: %v", err)
					fmt.Fprintf(s.Conn, "550 Upload failed: %v\r\n", err)
					return false
				}

				if fileSize == 0 && zipscript.ShouldDeleteZeroByteForDir(s.Config.Zipscript, uploadDir) {
					bridge.DeleteFile(filePath)
					log.Printf("[MASTER-ZS] Deleted 0-byte file: %s", filePath)
					fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
					return false
				}

				if checksum > 0 && zipscript.ShouldDeleteBadCRCForDir(s.Config.Zipscript, uploadDir) && !strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					sfvEntries := bridge.GetSFVData(uploadDir)
					if sfvEntries != nil {
						if expectedCRC, exists := cachedExpectedCRC(sfvEntries, fileName); exists {
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
						bridge.CacheSFV(uploadDir, fileName, sfvEntries)
					}
				}
				if err := refreshZipDIZFromArchive(bridge, uploadDir, filePath, fileName); err != nil && s.Config.Debug {
					log.Printf("[MASTER-ZS] zip diz refresh skipped for %s: %v", filePath, err)
				}
				if err := applyAudioZipscriptChecksForDir(s, bridge, uploadDir, filePath, fileName); err != nil {
					fmt.Fprintf(s.Conn, "226- zipscript audio check failed: %s\r\n", err)
					fmt.Fprintf(s.Conn, "226 Uploaded file removed by zipscript\r\n")
					return false
				}

				isSpeedtest := isSpeedtestPath(filePath)
				if fileSize > 0 {
					s.User.UpdateStatsWithCredits(fileSize, true, !isSpeedtest)
				}
				speedMB := 0.0
				if xferMs > 0 {
					speedMB = (float64(fileSize) / 1024.0 / 1024.0) / (float64(xferMs) / 1000.0)
				}
				data := map[string]string{}
				if subdir := zipscript.ReleaseSubdirLabel(s.Config.Zipscript, uploadDir); subdir != "" {
					data["release_subdir"] = subdir
					data["release_name"] = path.Base(path.Dir(uploadDir))
					if zipscript.IsIgnoredReleaseSubdir(s.Config.Zipscript, uploadDir) || !zipscript.AnnounceReleaseSubdirs(s.Config.Zipscript) {
						data["skip_release_announce"] = "true"
					}
				}
				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries := bridge.GetSFVData(uploadDir); sfvEntries != nil {
						data["t_filecount"] = fmt.Sprintf("%d", len(sfvEntries))
						data["t_file_label"] = zipscript.ExpectedFileLabel(s.Config.Zipscript, uploadDir)
					}
				}
				raceUsers, raceTotalBytes, raceTotalFiles, raceComplete := populateUploadRaceData(bridge, s.Config, uploadDir, fileName, fileSize, data)
				s.emitEvent(EventUpload, filePath, fileName, fileSize, speedMB, data)
				if shouldAnnounceNoRace(s.Config, uploadDir, existingNames, fileName) {
					go emitRaceEndAfter(s, uploadDir, nil, fileSize, 1, xferMs, 0)
				}
				if zipscript.UsesZip(s.Config.Zipscript, uploadDir) {
					expectedZipParts := zipExpectedPartsFromDIZ(bridge, uploadDir)
					if shouldEmitZipRaceEnd(s.Config, uploadDir, fileName) && zipDirComplete(bridge.ListDir(uploadDir), expectedZipParts) && raceTotalFiles > 0 {
						go emitZipRaceEndAfter(s, uploadDir, xferMs, zipscript.MediaInfoGraceDelayForDir(s.Config.Zipscript, uploadDir, fileName))
					}
				} else if sfvEntries := bridge.GetSFVData(uploadDir); sfvEntries != nil {
					if raceComplete && zipscript.CanTriggerRaceEndForDir(s.Config.Zipscript, uploadDir, sfvEntries, fileName) {
						// Race complete: fire COMPLETE/STATS sequence in a
						// goroutine so the client gets 226 immediately. The
						// FIFO writes + plugin dispatches were stacking up on
						// the connection's hot path and delaying the final
						// transfer ack by the time it took to do all that work.
						go emitRaceEndAfter(s, uploadDir, raceUsers, raceTotalBytes, raceTotalFiles, xferMs, zipscript.MediaInfoGraceDelayForDir(s.Config.Zipscript, uploadDir, fileName))
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
				filePath := uploadPath

				var fileSize int64
				var checksum uint32
				var xferMs int64

				if s.PassthruSlave != nil && s.Config.Passthrough {
					slaveName := s.PassthruSlave.(string)
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")
					log.Printf("[Passthrough] STOR %s via slave %s (xferIdx=%d)", filePath, slaveName, s.PassthruXferIdx)
					s.beginTransferOnSlave("upload", filePath, slaveName, s.PassthruXferIdx)
					defer s.endTransfer()

					fileSize, checksum, xferMs, err = bridge.SlaveReceivePassthrough(filePath, s.PassthruXferIdx, slaveName, s.User.Name, s.User.PrimaryGroup, restOffset)
					s.PassthruSlave = nil
					s.PretCmd = ""
					s.PretArg = ""

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
					s.beginTransfer("upload", filePath)
					defer s.endTransfer()
					dataConn = trackTransferConn(s, dataConn, "upload")

					start := time.Now()
					fileSize, checksum, err = bridge.UploadFile(filePath, dataConn, s.User.Name, s.User.PrimaryGroup, restOffset)
					xferMs = time.Since(start).Milliseconds()
					dataConn.Close()

					if err != nil {
						log.Printf("[MASTER] Upload failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Upload failed: %v\r\n", err)
						return false
					}
				}

				if fileSize == 0 && zipscript.ShouldDeleteZeroByteForDir(s.Config.Zipscript, uploadDir) {
					bridge.DeleteFile(filePath)
					log.Printf("[MASTER-ZS] Deleted 0-byte file: %s", filePath)
					fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
					return false
				}

				if checksum > 0 && zipscript.ShouldDeleteBadCRCForDir(s.Config.Zipscript, uploadDir) && !strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					sfvEntries := bridge.GetSFVData(uploadDir)
					if sfvEntries != nil {
						if expectedCRC, exists := cachedExpectedCRC(sfvEntries, fileName); exists {
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
						bridge.CacheSFV(uploadDir, fileName, sfvEntries)
					}
				}
				if err := refreshZipDIZFromArchive(bridge, uploadDir, filePath, fileName); err != nil && s.Config.Debug {
					log.Printf("[MASTER-ZS] zip diz refresh skipped for %s: %v", filePath, err)
				}
				if err := applyAudioZipscriptChecksForDir(s, bridge, uploadDir, filePath, fileName); err != nil {
					fmt.Fprintf(s.Conn, "226- zipscript audio check failed: %s\r\n", err)
					fmt.Fprintf(s.Conn, "226 Uploaded file removed by zipscript\r\n")
					return false
				}

				isSpeedtest := isSpeedtestPath(filePath)
				if fileSize > 0 {
					s.User.UpdateStatsWithCredits(fileSize, true, !isSpeedtest)
				}
				speedMB := 0.0
				if xferMs > 0 {
					speedMB = (float64(fileSize) / 1024.0 / 1024.0) / (float64(xferMs) / 1000.0)
				}
				data := map[string]string{}
				if subdir := zipscript.ReleaseSubdirLabel(s.Config.Zipscript, uploadDir); subdir != "" {
					data["release_subdir"] = subdir
					data["release_name"] = path.Base(path.Dir(uploadDir))
					if zipscript.IsIgnoredReleaseSubdir(s.Config.Zipscript, uploadDir) || !zipscript.AnnounceReleaseSubdirs(s.Config.Zipscript) {
						data["skip_release_announce"] = "true"
					}
				}
				if strings.HasSuffix(strings.ToLower(fileName), ".sfv") {
					if sfvEntries := bridge.GetSFVData(uploadDir); sfvEntries != nil {
						data["t_filecount"] = fmt.Sprintf("%d", len(sfvEntries))
						data["t_file_label"] = zipscript.ExpectedFileLabel(s.Config.Zipscript, uploadDir)
					}
				}
				raceUsers, raceTotalBytes, raceTotalFiles, raceComplete := populateUploadRaceData(bridge, s.Config, uploadDir, fileName, fileSize, data)
				s.emitEvent(EventUpload, filePath, fileName, fileSize, speedMB, data)
				if shouldAnnounceNoRace(s.Config, uploadDir, existingNames, fileName) {
					go emitRaceEndAfter(s, uploadDir, nil, fileSize, 1, xferMs, 0)
				}
				if zipscript.UsesZip(s.Config.Zipscript, uploadDir) {
					expectedZipParts := zipExpectedPartsFromDIZ(bridge, uploadDir)
					if shouldEmitZipRaceEnd(s.Config, uploadDir, fileName) && zipDirComplete(bridge.ListDir(uploadDir), expectedZipParts) && raceTotalFiles > 0 {
						go emitZipRaceEndAfter(s, uploadDir, xferMs, zipscript.MediaInfoGraceDelayForDir(s.Config.Zipscript, uploadDir, fileName))
					}
				} else if sfvEntries := bridge.GetSFVData(uploadDir); sfvEntries != nil {
					if raceComplete && zipscript.CanTriggerRaceEndForDir(s.Config.Zipscript, uploadDir, sfvEntries, fileName) {
						// Async - see explanation at the other emitRaceEnd call.
						go emitRaceEndAfter(s, uploadDir, raceUsers, raceTotalBytes, raceTotalFiles, xferMs, zipscript.MediaInfoGraceDelayForDir(s.Config.Zipscript, uploadDir, fileName))
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

		filePath := path.Clean(path.Join(s.CurrentDir, args[0]))
		if path.IsAbs(strings.TrimSpace(args[0])) {
			filePath = path.Clean(args[0])
		}
		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath = bridge.ResolvePath(filePath)
			}
		}
		aclPath := path.Join(s.Config.ACLBasePath, filePath)
		if !s.ACLEngine.CanPerform(s.User, "DOWNLOAD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied.\r\n")
			return false
		}
		restOffset := s.RestOffset
		s.RestOffset = 0

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				fileSize := bridge.GetFileSize(filePath)
				if fileSize < 0 {
					fmt.Fprintf(s.Conn, "550 File not found on any slave.\r\n")
					return false
				}
				if restOffset > fileSize {
					fmt.Fprintf(s.Conn, "550 Resume offset beyond end of file.\r\n")
					return false
				}
				isSpeedtest := isSpeedtestPath(filePath)
				remainingSize := fileSize
				if restOffset > 0 && restOffset < fileSize {
					remainingSize = fileSize - restOffset
				}
				if !isSpeedtest && !s.User.CanDownload("", remainingSize) {
					fmt.Fprintf(s.Conn, "550 Not enough credits.\r\n")
					return false
				}

				if s.PassthruSlave != nil && s.Config.Passthrough {
					slaveName := s.PassthruSlave.(string)
					fmt.Fprintf(s.Conn, "150 Opening binary mode data connection for %s (%d bytes).\r\n", args[0], fileSize)
					log.Printf("[Passthrough] RETR %s via slave %s (xferIdx=%d)", filePath, slaveName, s.PassthruXferIdx)
					s.beginTransferOnSlave("download", filePath, slaveName, s.PassthruXferIdx)
					defer s.endTransfer()

					start := time.Now()
					err := bridge.SlaveSendPassthrough(filePath, s.PassthruXferIdx, slaveName, restOffset)
					xferMs := time.Since(start).Milliseconds()
					s.PassthruSlave = nil
					s.PretCmd = ""
					s.PretArg = ""

					if err != nil {
						log.Printf("[Passthrough] Download failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Download failed: %v\r\n", err)
					} else {
						fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
						if remainingSize > 0 {
							s.User.UpdateStatsWithCredits(remainingSize, false, !isSpeedtest)
						}
						s.emitEvent(EventDownload, filePath, args[0], remainingSize, transferSpeedMB(remainingSize, xferMs), nil)
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
					s.beginTransfer("download", filePath)
					defer s.endTransfer()
					dataConn = trackTransferConn(s, dataConn, "download")
					start := time.Now()
					err = bridge.DownloadFile(filePath, dataConn, restOffset)
					xferMs := time.Since(start).Milliseconds()
					dataConn.Close()
					s.PretCmd = ""
					s.PretArg = ""
					if err != nil {
						log.Printf("[MASTER] Download failed: %v", err)
						fmt.Fprintf(s.Conn, "550 Download failed: %v\r\n", err)
					} else {
						fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
						if remainingSize > 0 {
							s.User.UpdateStatsWithCredits(remainingSize, false, !isSpeedtest)
						}
						s.emitEvent(EventDownload, filePath, args[0], remainingSize, transferSpeedMB(remainingSize, xferMs), nil)
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
				for _, marker := range incompleteMarkerEntries(bridge, s.Config, activeIncompleteIndicator(s.Config), target, entries) {
					ts := timeutil.Unix(marker.ModTime).Format("Jan _2 15:04")
					fmt.Fprintf(s.Conn, " %s   1 %-8s %-8s %10s %s %s -> %s\r\n",
						ftpListMode(marker), marker.Owner, marker.Group, "0", ts, marker.Name, marker.LinkTarget)
				}
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
					ts := timeutil.Unix(e.ModTime).Format("Jan _2 15:04")
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
		fmt.Fprintf(s.Conn, "%s  [Credits: %.1fGiB] [Ratio: %s]\r\n",
			code, creditsGiB, ratioStr)
	} else {
		fmt.Fprintf(s.Conn, "%s- [Credits: %.1fGiB] [Ratio: %s]\r\n",
			code, creditsGiB, ratioStr)
	}
}

func mbString(size int64) string { return fmt.Sprintf("%.0fMB", float64(size)/1024.0/1024.0) }

func isSpeedtestPath(p string) bool {
	clean := strings.ToLower(path.Clean("/" + strings.TrimSpace(p)))
	return clean == "/speedtest" || strings.HasPrefix(clean, "/speedtest/")
}

func transferSpeedMB(size int64, xferMs int64) float64 {
	if size <= 0 || xferMs <= 0 {
		return 0
	}
	return (float64(size) / 1024.0 / 1024.0) / (float64(xferMs) / 1000.0)
}

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

func mlsdSymlinkType(e MasterFileEntry) string {
	target := strings.TrimSpace(e.LinkTarget)
	if target == "" {
		return "OS.unix=symlink"
	}
	return "OS.unix=slink:" + target
}

func incompleteMarkerName(pattern, relname string) string {
	pattern = strings.TrimSpace(pattern)
	relname = strings.TrimSpace(relname)
	if pattern == "" || relname == "" {
		return ""
	}
	if strings.Contains(pattern, "%0") {
		return path.Base(strings.ReplaceAll(pattern, "%0", relname))
	}
	return path.Base(pattern)
}

func incompleteMarkerName2(pattern, relname, child string) string {
	pattern = incompleteMarkerName(pattern, relname)
	if strings.Contains(pattern, "%1") {
		pattern = strings.ReplaceAll(pattern, "%1", strings.TrimSpace(child))
	}
	return pattern
}

func isIncompleteMarkerName(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return strings.HasPrefix(strings.ToLower(name), "[incomplete]")
	}
	if strings.Contains(pattern, "%0") {
		prefix := path.Base(strings.SplitN(pattern, "%0", 2)[0])
		return prefix != "" && strings.HasPrefix(name, prefix)
	}
	return name == path.Base(pattern)
}

func incompleteMarkerEntries(bridge MasterBridge, cfg *Config, pattern, dirPath string, entries []MasterFileEntry) []MasterFileEntry {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || cfg == nil {
		return nil
	}
	cleanDirPath := path.Clean("/" + strings.TrimSpace(dirPath))
	if cleanDirPath == "/" {
		return nil
	}
	existing := make(map[string]bool, len(entries))
	for _, e := range entries {
		existing[e.Name] = true
	}
	out := []MasterFileEntry{}
	for _, e := range entries {
		if !e.IsDir || e.IsSymlink || strings.HasPrefix(e.Name, ".") || isIncompleteMarkerName(pattern, e.Name) {
			continue
		}
		releasePath := path.Join(dirPath, e.Name)
		if !zipscript.UsesReleaseCheckEntry(cfg.Zipscript, releasePath) {
			continue
		}
		if zipscript.IsIgnoredReleaseSubdir(cfg.Zipscript, releasePath) {
			continue
		}
		releaseEntries := bridge.ListDir(releasePath)
		present, total := 0, 0
		if zipscript.UsesZip(cfg.Zipscript, releasePath) {
			expected := zipExpectedPartsFromDIZ(bridge, releasePath)
			_, _, present = zipDirRaceStats(releaseEntries, expected)
			if expected > 0 {
				total = expected
			}
		} else {
			_, _, _, present, total = bridge.GetVFSRaceStats(releasePath)
		}

		noSFVPattern := zipscript.NoSFVIndicator(cfg.Zipscript)
		if noSFVPattern != "" && !zipscript.UsesZip(cfg.Zipscript, releasePath) && !hasSFVEntry(releaseEntries) {
			marker := incompleteMarkerName(noSFVPattern, e.Name)
			if marker != "" && !existing[marker] {
				out = append(out, MasterFileEntry{
					Name:       marker,
					IsSymlink:  true,
					LinkTarget: releasePath,
					ModTime:    e.ModTime,
					Owner:      "GoFTPd",
					Group:      "GoFTPd",
				})
				existing[marker] = true
			}
		}
		nfoPattern := zipscript.NFOIndicator(cfg.Zipscript)
		if nfoPattern != "" && !hasNFOEntry(releaseEntries) {
			marker := incompleteMarkerName(nfoPattern, e.Name)
			if marker != "" && !existing[marker] {
				out = append(out, MasterFileEntry{
					Name:       marker,
					IsSymlink:  true,
					LinkTarget: releasePath,
					ModTime:    e.ModTime,
					Owner:      "GoFTPd",
					Group:      "GoFTPd",
				})
				existing[marker] = true
			}
		}

		emptyDir := false
		if total <= 0 {
			if zipscript.MarkEmptyDirsOnRescan(cfg.Zipscript) {
				visible := 0
				for _, child := range releaseEntries {
					if !strings.HasPrefix(child.Name, ".") {
						visible++
					}
				}
				emptyDir = visible == 0
			}
			if !emptyDir {
				continue
			}
		}
		if total > 0 && present < total {
			marker := incompleteMarkerName(pattern, e.Name)
			if marker != "" && !existing[marker] {
				out = append(out, MasterFileEntry{
					Name:       marker,
					IsSymlink:  true,
					LinkTarget: releasePath,
					ModTime:    e.ModTime,
					Owner:      "GoFTPd",
					Group:      "GoFTPd",
				})
				existing[marker] = true
			}
		}
		cdPattern := zipscript.CDIndicator(cfg.Zipscript)
		if cdPattern != "" && isDiscDirName(e.Name) {
			_, _, _, childPresent, childTotal := bridge.GetVFSRaceStats(releasePath)
			if childTotal > 0 && childPresent < childTotal {
				marker := incompleteMarkerName2(cdPattern, path.Base(dirPath), e.Name)
				if marker != "" && !existing[marker] {
					out = append(out, MasterFileEntry{
						Name:       marker,
						IsSymlink:  true,
						LinkTarget: releasePath,
						ModTime:    e.ModTime,
						Owner:      "GoFTPd",
						Group:      "GoFTPd",
					})
					existing[marker] = true
				}
			}
		}
	}
	return out
}

func resolveIncompleteMarkerTarget(bridge MasterBridge, cfg *Config, pattern, parent, name string) string {
	for _, marker := range incompleteMarkerEntries(bridge, cfg, pattern, parent, bridge.ListDir(parent)) {
		if marker.Name == name && marker.LinkTarget != "" {
			return path.Clean(marker.LinkTarget)
		}
	}
	return ""
}

func hasNFOEntry(entries []MasterFileEntry) bool {
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name), ".nfo") {
			return true
		}
	}
	return false
}

func hasSFVEntry(entries []MasterFileEntry) bool {
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name), ".sfv") {
			return true
		}
	}
	return false
}

func isDiscDirName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	ok, _ := regexp.MatchString(`^(cd|disc|disk|dvd)\d+$`, lower)
	return ok
}

func progressBar(present, total, width int) string {
	if total <= 0 {
		total = 1
	}
	if width <= 0 {
		width = 20
	}
	filled := (present * width) / total
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	var b strings.Builder
	b.WriteByte('[')
	for i := 0; i < width; i++ {
		if i < filled {
			b.WriteByte('#')
		} else {
			b.WriteByte(':')
		}
	}
	b.WriteByte(']')
	return b.String()
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func currentRaceSpeedMB(dirPath string, totalBytes int64, bridge MasterBridge) float64 {
	if bridge == nil || totalBytes <= 0 {
		return 0
	}
	ms := bridge.GetRaceWallClockMilliseconds(dirPath)
	if ms <= 0 {
		return 0
	}
	return (float64(totalBytes) / 1024.0 / 1024.0) / (float64(ms) / 1000.0)
}

func estimateRaceTimeLeft(dirPath string, totalBytes int64, present, total int, bridge MasterBridge) string {
	if totalBytes <= 0 || present <= 0 || total <= present {
		return "0s"
	}
	speed := currentRaceSpeedMB(dirPath, totalBytes, bridge)
	if speed <= 0 {
		return "N/A"
	}
	avgBytesPerFile := float64(totalBytes) / float64(present)
	bytesLeft := avgBytesPerFile * float64(total-present)
	seconds := int((bytesLeft / 1024.0 / 1024.0) / speed)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%ds", seconds)
}

func estimateZipTimeLeft(dirPath string, totalBytes int64, present, total int, bridge MasterBridge) string {
	if totalBytes <= 0 || present <= 0 || total <= present {
		return "0s"
	}
	speed := currentRaceSpeedMB(dirPath, totalBytes, bridge)
	if speed <= 0 {
		return "N/A"
	}
	avgBytesPerFile := float64(totalBytes) / float64(present)
	bytesLeft := avgBytesPerFile * float64(total-present)
	seconds := int((bytesLeft / 1024.0 / 1024.0) / speed)
	if seconds < 1 {
		seconds = 1
	}
	return fmt.Sprintf("%ds", seconds)
}

func dirRaceProgress(bridge MasterBridge, cfg *Config, dirPath string) (totalBytes int64, present int, total int) {
	if bridge == nil || cfg == nil {
		return 0, 0, 0
	}
	if zipscript.UsesZip(cfg.Zipscript, dirPath) {
		entries := bridge.ListDir(dirPath)
		expected := zipExpectedPartsFromDIZ(bridge, dirPath)
		_, totalBytes, present = zipDirRaceStats(entries, expected)
		if expected > 0 {
			total = expected
		} else {
			total = present
		}
		return totalBytes, present, total
	}
	_, _, totalBytes, present, total = bridge.GetVFSRaceStats(dirPath)
	return totalBytes, present, total
}

func dirRaceStatusName(bridge MasterBridge, cfg *Config, dirPath, siteName string) string {
	if !raceStatusEligibleDir(dirPath) {
		return ""
	}
	totalBytes, present, total := dirRaceProgress(bridge, cfg, dirPath)
	if total <= 0 {
		return ""
	}
	totalMB := float64(totalBytes) / (1024 * 1024)
	if present >= total {
		return zipscript.CompleteStatusName(cfg.Zipscript, siteName, dirPath, totalMB, total, bridge)
	}
	pct := (present * 100) / total
	return fmt.Sprintf("%s - %3d%% Complete - [%s]", progressBar(present, total, 20), pct, siteName)
}

func raceStatusEligibleDir(dirPath string) bool {
	cleaned := path.Clean("/" + strings.TrimSpace(dirPath))
	if cleaned == "/" || cleaned == "." {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(cleaned, "/"), "/")
	if len(parts) < 2 {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(parts[0])) {
	case "FOREIGN", "PRE", "ARCHIVE":
		return len(parts) >= 3
	}
	return true
}

func emitRaceEndAfter(s *Session, dirPath string, users []VFSRaceUser, totalBytes int64, total int, xferMs int64, delay time.Duration) {
	if delay > 0 {
		time.Sleep(delay)
	}
	emitRaceEnd(s, dirPath, users, totalBytes, total, xferMs)
}

func emitZipRaceEndAfter(s *Session, dirPath string, xferMs int64, delay time.Duration) {
	if s == nil || s.Config == nil {
		return
	}
	if delay < 200*time.Millisecond {
		delay = 200 * time.Millisecond
	}
	if delay > 0 {
		time.Sleep(delay)
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		return
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		return
	}
	if bridge.GetFileSize(path.Join(dirPath, "file_id.diz")) < 0 {
		if _, err := recoverZipDIZFromDirectory(bridge, dirPath); err != nil && s.Config.Debug {
			log.Printf("[MASTER-ZS] delayed zip diz recovery skipped for %s: %v", dirPath, err)
		}
	}
	expected := zipExpectedPartsFromDIZ(bridge, dirPath)
	entries := bridge.ListDir(dirPath)
	if !zipDirComplete(entries, expected) {
		return
	}
	users, totalBytes, total := zipDirRaceStats(entries, expected)
	if total <= 0 {
		return
	}
	emitRaceEnd(s, dirPath, users, totalBytes, total, xferMs)
}

func zipscriptExistingNames(bridge MasterBridge, dirPath string) []string {
	entries := bridge.ListDir(dirPath)
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name)
	}
	return out
}

func activeIncompleteIndicator(cfg *Config) string {
	if cfg == nil {
		return ""
	}
	return zipscript.IncompleteIndicator(cfg.Zipscript, "")
}

func trimRaceUsers(cfg *Config, users []VFSRaceUser) []VFSRaceUser {
	if cfg == nil || cfg.Zipscript.Race.MaxUsersInTop <= 0 || len(users) <= cfg.Zipscript.Race.MaxUsersInTop {
		return users
	}
	return users[:cfg.Zipscript.Race.MaxUsersInTop]
}

func trimRaceGroups(cfg *Config, groups []VFSRaceGroup) []VFSRaceGroup {
	if cfg == nil || cfg.Zipscript.Race.MaxGroupsInTop <= 0 || len(groups) <= cfg.Zipscript.Race.MaxGroupsInTop {
		return groups
	}
	return groups[:cfg.Zipscript.Race.MaxGroupsInTop]
}

func raceCRCKey(name string) string {
	name = strings.TrimSpace(path.Base(strings.ReplaceAll(name, "\\", "/")))
	return strings.ToLower(name)
}

func cachedExpectedCRC(sfvEntries map[string]uint32, fileName string) (uint32, bool) {
	if sfvEntries == nil {
		return 0, false
	}
	crc, ok := sfvEntries[raceCRCKey(fileName)]
	return crc, ok
}

func zipscriptMediaInfoSettings(cfg *Config) (string, int) {
	if cfg != nil && cfg.Plugins != nil {
		if pluginCfg, ok := cfg.Plugins["mediainfo"]; ok {
			binary := "mediainfo"
			timeoutSeconds := 10
			if raw, ok := pluginCfg["binary"].(string); ok && strings.TrimSpace(raw) != "" {
				binary = strings.TrimSpace(raw)
			}
			switch v := pluginCfg["timeout_seconds"].(type) {
			case int:
				timeoutSeconds = v
			case int64:
				timeoutSeconds = int(v)
			case float64:
				timeoutSeconds = int(v)
			}
			if timeoutSeconds <= 0 {
				timeoutSeconds = 10
			}
			return binary, timeoutSeconds
		}
	}
	return "mediainfo", 10
}

func applyAudioZipscriptChecks(s *Session, bridge MasterBridge, filePath, fileName string) error {
	return applyAudioZipscriptChecksForDir(s, bridge, s.CurrentDir, filePath, fileName)
}

func applyAudioZipscriptChecksForDir(s *Session, bridge MasterBridge, dirPath, filePath, fileName string) error {
	if !zipscript.AudioCheckEnabled(s.Config.Zipscript, dirPath, fileName) {
		return nil
	}
	binary, timeoutSeconds := zipscriptMediaInfoSettings(s.Config)
	fields, err := bridge.ProbeMediaInfo(filePath, binary, timeoutSeconds)
	if err != nil {
		if s.Config.Debug {
			log.Printf("[MASTER-ZS] mediainfo probe skipped for %s: %v", filePath, err)
		}
		return nil
	}
	bridge.CacheMediaInfo(dirPath, fields)
	if reasons := zipscript.ValidateAudioRelease(s.Config.Zipscript, fields); len(reasons) > 0 {
		_ = bridge.DeleteFile(filePath)
		return fmt.Errorf(strings.Join(reasons, "; "))
	}
	if err := ensureAudioSortLinks(bridge, zipscript.AudioSortLinks(s.Config.Zipscript, dirPath, fields)); err != nil && s.Config.Debug {
		log.Printf("[MASTER-ZS] audio sort link failed for %s: %v", dirPath, err)
	}
	return nil
}

func ensureAudioSortLinks(bridge MasterBridge, links []zipscript.AudioSortLink) error {
	for _, link := range links {
		if err := ensureDirPath(bridge, link.DirPath); err != nil {
			return err
		}
		if err := bridge.Symlink(link.LinkPath, link.Target); err != nil {
			return err
		}
	}
	return nil
}

func ensureDirPath(bridge MasterBridge, dirPath string) error {
	dirPath = path.Clean("/" + strings.TrimSpace(dirPath))
	if dirPath == "/" || dirPath == "." {
		return nil
	}
	parts := strings.Split(strings.TrimPrefix(dirPath, "/"), "/")
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current = path.Join(current, "/"+part)
		if !bridge.FileExists(current) {
			bridge.MakeDir(current, "GoFTPd", "GoFTPd")
		}
	}
	return nil
}

func shouldAnnounceNoRace(cfg *Config, dirPath string, existingNames []string, fileName string) bool {
	if cfg == nil || !cfg.Zipscript.Enabled || !cfg.Zipscript.Race.AnnounceNoRace {
		return false
	}
	if zipscript.IsIgnoredReleaseSubdir(cfg.Zipscript, dirPath) {
		return false
	}
	if zipscript.UsesRace(cfg.Zipscript, dirPath) || zipscript.IsIgnoredType(cfg.Zipscript, fileName) {
		return false
	}
	if strings.HasPrefix(strings.TrimSpace(fileName), ".") {
		return false
	}
	for _, name := range existingNames {
		name = strings.TrimSpace(name)
		if name == "" || strings.HasPrefix(name, ".") || zipscript.IsIgnoredType(cfg.Zipscript, name) {
			continue
		}
		return false
	}
	return true
}

func isZipPayloadName(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	return regexp.MustCompile(`(?i)\.(zip|z\d\d)$`).MatchString(name)
}

func zipDirRaceStats(entries []MasterFileEntry, expectedTotal int) ([]VFSRaceUser, int64, int) {
	userMap := make(map[string]*VFSRaceUser)
	totalBytes := int64(0)
	total := 0
	for _, e := range entries {
		if e.IsDir || e.IsSymlink || strings.HasPrefix(strings.TrimSpace(e.Name), ".") || !isZipPayloadName(e.Name) {
			continue
		}
		total++
		totalBytes += e.Size
		owner := e.Owner
		if owner == "" {
			owner = "unknown"
		}
		group := e.Group
		if group == "" {
			group = "NoGroup"
		}
		us := userMap[owner]
		if us == nil {
			us = &VFSRaceUser{Name: owner, Group: group}
			userMap[owner] = us
		}
		us.Files++
		us.Bytes += e.Size
		if e.XferTime > 0 {
			fileSpeed := float64(e.Size) / (float64(e.XferTime) / 1000.0)
			us.Speed += fileSpeed
			if fileSpeed > us.PeakSpeed {
				us.PeakSpeed = fileSpeed
			}
			if us.SlowSpeed == 0 || fileSpeed < us.SlowSpeed {
				us.SlowSpeed = fileSpeed
			}
			us.DurationMs += e.XferTime
		}
	}
	users := make([]VFSRaceUser, 0, len(userMap))
	for _, us := range userMap {
		percentBase := total
		if expectedTotal > 0 {
			percentBase = expectedTotal
		}
		if percentBase > 0 {
			us.Percent = (us.Files * 100) / percentBase
		}
		if us.Files > 0 {
			us.Speed = us.Speed / float64(us.Files)
		}
		users = append(users, *us)
	}
	sort.Slice(users, func(i, j int) bool {
		if users[i].Files != users[j].Files {
			return users[i].Files > users[j].Files
		}
		if users[i].Bytes != users[j].Bytes {
			return users[i].Bytes > users[j].Bytes
		}
		return strings.ToLower(users[i].Name) < strings.ToLower(users[j].Name)
	})
	return users, totalBytes, total
}

func zipDirCurrentPartState(entries []MasterFileEntry) (total int, highestDigit int, highestLetter int, mode string, ok bool) {
	total = 0
	highestDigit = 0
	highestLetter = 0
	mode = ""
	for _, e := range entries {
		if e.IsDir || e.IsSymlink || strings.HasPrefix(strings.TrimSpace(e.Name), ".") || !isZipPayloadName(e.Name) {
			continue
		}
		total++
		base := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(e.Name)), ".zip")
		if m := regexp.MustCompile(`(\d+)$`).FindStringSubmatch(base); len(m) == 2 {
			n, err := strconv.Atoi(m[1])
			if err != nil || n <= 0 {
				return 0, 0, 0, "", false
			}
			if mode == "" {
				mode = "digit"
			}
			if mode != "digit" {
				return 0, 0, 0, "", false
			}
			if n > highestDigit {
				highestDigit = n
			}
			continue
		}
		if m := regexp.MustCompile(`([a-z])$`).FindStringSubmatch(base); len(m) == 2 {
			n := int(m[1][0]-'a') + 1
			if n <= 0 {
				return 0, 0, 0, "", false
			}
			if mode == "" {
				mode = "letter"
			}
			if mode != "letter" {
				return 0, 0, 0, "", false
			}
			if n > highestLetter {
				highestLetter = n
			}
			continue
		}
		return 0, 0, 0, "", false
	}
	if total == 0 || mode == "" {
		return 0, 0, 0, "", false
	}
	return total, highestDigit, highestLetter, mode, true
}

func zipExpectedPartsFromDIZ(bridge MasterBridge, dirPath string) int {
	content, err := bridge.ReadFile(path.Join(dirPath, "file_id.diz"))
	if err != nil || len(content) == 0 {
		recovered, recoverErr := recoverZipDIZFromDirectory(bridge, dirPath)
		if recoverErr != nil || len(recovered) == 0 {
			return 0
		}
		content = recovered
	}
	return zipExpectedPartsFromDIZContent(content)
}

func zipExpectedPartsFromDIZContent(content []byte) int {
	if len(content) == 0 {
		return 0
	}
	text := normalizeZipDIZText(string(content))
	if text == "" {
		return 0
	}

	patterns := []string{
		"[?!/##]",
		"(?!/##)",
		"[?!!/###]",
		"(?!!/###)",
		"[?/#]",
		"[?/##]",
		"(?/#)",
		"[disk:!!/##]",
		"[disk:?!/##]",
		"o?/o#",
		"disks[!!/##",
		" !/# ",
		" !!/##&/&!",
		"&/!!/## ",
		"[!!/#]",
		": ?!/##&/",
		"xx/##",
		"<!!/##>",
		"x/##",
		"! of #",
		"? of #",
		"x of #",
		"ox of o#",
		"!! of ##",
		"?! of ##",
		"xx of ##",
	}

	best := 0
	for start := 0; start < len(text); start++ {
		for _, pattern := range patterns {
			total, ok := matchZipDIZPattern(text, start, pattern)
			if ok && total > best {
				best = total
			}
		}
	}
	return best
}

func normalizeZipDIZText(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	lastSpace := false
	for _, r := range text {
		switch r {
		case '\x00', '\r', '\n', '\t', ' ':
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		default:
			b.WriteRune(unicode.ToLower(r))
			lastSpace = false
		}
	}
	return b.String()
}

func matchZipDIZPattern(text string, start int, pattern string) (int, bool) {
	if start >= len(text) {
		return 0, false
	}
	var totalDigits strings.Builder
	control := 0
	matches := 0
	for patIdx := 0; patIdx <= len(pattern)-control-1; patIdx++ {
		textIdx := start + patIdx
		if textIdx >= len(text) {
			return 0, false
		}
		ch := text[textIdx]
		token := pattern[patIdx+control]
		switch token {
		case '#':
			if (ch >= '0' && ch <= '9') || ch == ' ' || ch == 'o' {
				if ch == 'o' {
					ch = '0'
				}
				matches++
				totalDigits.WriteByte(ch)
			}
		case '?':
			matches++
		case '!':
			if (ch >= '0' && ch <= '9') || ch == 'o' || ch == 'x' {
				matches++
			}
		case '&':
			control++
			if patIdx+control >= len(pattern) {
				return 0, false
			}
			next := pattern[patIdx+control]
			if !(next == '!' && ((ch >= '0' && ch <= '9') || ch == 'o' || ch == 'x')) && ch != next {
				matches++
			}
		default:
			if token == ch {
				matches++
			}
		}
	}
	if matches != len(pattern)-control {
		return 0, false
	}
	raw := strings.TrimSpace(totalDigits.String())
	if raw == "" {
		return 0, false
	}
	total, err := strconv.Atoi(raw)
	if err != nil || total <= 0 {
		return 0, false
	}
	return total, true
}

func recoverZipDIZFromDirectory(bridge MasterBridge, dirPath string) ([]byte, error) {
	if bridge == nil {
		return nil, fmt.Errorf("bridge unavailable")
	}
	entries := bridge.ListDir(dirPath)
	var archives []string
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if isZipPayloadName(entry.Name) {
			archives = append(archives, path.Join(dirPath, entry.Name))
		}
	}
	sort.Strings(archives)
	for _, archivePath := range archives {
		content, err := bridge.ReadZipEntry(archivePath, "file_id.diz")
		if err != nil || len(content) == 0 {
			continue
		}
		if writeErr := bridge.WriteFile(path.Join(dirPath, "file_id.diz"), content); writeErr != nil {
			return nil, writeErr
		}
		return content, nil
	}
	return nil, fmt.Errorf("file_id.diz not found in any archive")
}

func shouldBlockZipDIZUpload(cfg *Config, dirPath, fileName string) bool {
	if cfg == nil || !zipscript.UsesZip(cfg.Zipscript, dirPath) {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(fileName), "file_id.diz")
}

func refreshZipDIZFromArchive(bridge MasterBridge, dirPath, archivePath, fileName string) error {
	if bridge == nil || !isZipPayloadName(fileName) {
		return nil
	}
	dizPath := path.Join(dirPath, "file_id.diz")
	if bridge.GetFileSize(dizPath) >= 0 {
		return nil
	}
	content, err := bridge.ReadZipEntry(archivePath, "file_id.diz")
	if err != nil || len(content) == 0 {
		return err
	}
	return bridge.WriteFile(dizPath, content)
}

func zipDirComplete(entries []MasterFileEntry, expected int) bool {
	total, highestDigit, highestLetter, mode, ok := zipDirCurrentPartState(entries)
	if !ok {
		return false
	}
	if expected > 0 {
		return total == expected
	}
	switch mode {
	case "digit":
		return total > 0 && highestDigit == total
	case "letter":
		return total > 0 && highestLetter == total
	default:
		return false
	}
}

func populateUploadRaceData(bridge MasterBridge, cfg *Config, dirPath, fileName string, fileSize int64, data map[string]string) ([]VFSRaceUser, int64, int, bool) {
	sfvEntries := bridge.GetSFVData(dirPath)
	isTrackedPayload := zipscript.IsRacePayloadFileForDir(cfg.Zipscript, dirPath, fileName)
	if sfvEntries != nil {
		_, isTrackedPayload = sfvEntries[strings.ToLower(strings.TrimSpace(path.Base(strings.ReplaceAll(fileName, "\\", "/"))))]
		if !isTrackedPayload {
			isTrackedPayload = zipscript.IsRacePayloadFileForDir(cfg.Zipscript, dirPath, fileName)
		}
	}
	if !isTrackedPayload {
		return nil, 0, 0, false
	}
	data["file_mbytes"] = mbString(fileSize)
	if zipscript.UsesZip(cfg.Zipscript, dirPath) {
		expected := zipExpectedPartsFromDIZ(bridge, dirPath)
		users, totalBytes, total := zipDirRaceStats(bridge.ListDir(dirPath), expected)
		if total > 0 {
			data["relname"] = path.Base(dirPath)
			if expected > 0 {
				data["t_files"] = fmt.Sprintf("%d", expected)
				data["t_present"] = fmt.Sprintf("%d", total)
				data["t_filesleft"] = fmt.Sprintf("%d", maxInt(0, expected-total))
			} else {
				delete(data, "t_files")
				delete(data, "t_present")
				delete(data, "t_filesleft")
			}
			data["t_totalmb"] = fmt.Sprintf("%.1f", float64(totalBytes)/1024.0/1024.0)
			data["t_avgspeed"] = fmt.Sprintf("%.2fMB/s", currentRaceSpeedMB(dirPath, totalBytes, bridge))
			if expected > 0 && expected > total {
				data["t_timeleft"] = estimateZipTimeLeft(dirPath, totalBytes, total, expected, bridge)
			} else if expected > 0 {
				data["t_timeleft"] = "0s"
			} else {
				delete(data, "t_timeleft")
			}
			data["t_mbytes"] = fmt.Sprintf("%.0fMB", float64(totalBytes)/1024.0/1024.0)
			if len(users) > 0 {
				leader := users[0]
				data["leader_name"] = leader.Name
				data["leader_group"] = leader.Group
				data["leader_files"] = fmt.Sprintf("%d", leader.Files)
				data["leader_mb"] = fmt.Sprintf("%.1f", float64(leader.Bytes)/1024.0/1024.0)
				data["leader_pct"] = fmt.Sprintf("%d", leader.Percent)
				data["leader_speed"] = fmt.Sprintf("%.2fMB/s", leader.Speed/1024.0/1024.0)
			}
			return users, totalBytes, total, expected == 0 || total >= expected
		}
		return nil, 0, 0, false
	}
	if sfvEntries != nil {
		users, _, totalBytes, present, total := bridge.GetVFSRaceStats(dirPath)
		if total > 0 {
			data["relname"] = path.Base(dirPath)
			data["t_files"] = fmt.Sprintf("%d", total)
			data["t_present"] = fmt.Sprintf("%d", present)
			data["t_filesleft"] = fmt.Sprintf("%d", maxInt(0, total-present))
			data["t_totalmb"] = fmt.Sprintf("%.1f", float64(totalBytes)/1024.0/1024.0)
			data["t_avgspeed"] = fmt.Sprintf("%.2fMB/s", currentRaceSpeedMB(dirPath, totalBytes, bridge))
			data["t_timeleft"] = estimateRaceTimeLeft(dirPath, totalBytes, present, total, bridge)
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
			return users, totalBytes, total, present >= total
		}
	}
	return nil, 0, 0, false
}

func shouldEmitZipRaceEnd(cfg *Config, dirPath, fileName string) bool {
	if cfg == nil || !zipscript.UsesZip(cfg.Zipscript, dirPath) {
		return false
	}
	return strings.HasSuffix(strings.ToLower(strings.TrimSpace(fileName)), ".zip")
}
