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
		fmt.Fprintf(s.Conn, " REST STREAM\r\n")
		fmt.Fprintf(s.Conn, " SSCN\r\n")
		fmt.Fprintf(s.Conn, " CPSV\r\n")
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
		fmt.Fprintf(s.Conn, "200 CPSV command successful.\r\n")

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
		aclPath := path.Join(s.Config.ACLBasePath, s.CurrentDir, args[0])
		if !s.ACLEngine.CanPerform(s.User, "MKD", aclPath) {
			fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
			return false
		}

		dirName := args[0]
		if s.DupeChecker != nil {
			if dc, ok := s.DupeChecker.(interface{ IsDupe(string) (bool, error) }); ok {
				if isDupe, err := dc.IsDupe(dirName); err == nil && isDupe {
					fmt.Fprintf(s.Conn, "550 %s: directory already exists in dupe database.\r\n", dirName)
					return false
				}
			}
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				dirPath := path.Join(s.CurrentDir, args[0])
				bridge.MakeDir(dirPath, s.User.Name, s.User.PrimaryGroup)
			}
		}

		if s.DupeChecker != nil {
			if dc, ok := s.DupeChecker.(interface {
				AddDupe(string, string, string, int, int64) error
			}); ok {
				dc.AddDupe(dirName, s.User.PrimaryGroup, s.User.Name, 0, 0)
			}
		}

		fmt.Fprintf(s.Conn, "257 \"%s\" created\r\n", args[0])

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
			log.Printf("[PASV] Starting passive mode setup")
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

	case "MLSD":
		if s.Config.Debug {
			log.Printf("[MLSD] Client requesting machine list for %s", s.CurrentDir)
		}
		raw, err := s.getRawDataConn()
		if err != nil {
			fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
			return false
		}
		fmt.Fprintf(s.Conn, "150 File status okay; about to open data connection.\r\n")
		dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
		if err != nil {
			raw.Close()
			fmt.Fprintf(s.Conn, "435 Failed TLS negotiation on data channel\r\n")
			return false
		}

		mlsdPath := filepath.Join(s.Config.StoragePath, s.CurrentDir)
		files, err := os.ReadDir(mlsdPath)
		var output strings.Builder

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

		dataConn.Write([]byte(output.String()))
		dataConn.Close()
		fmt.Fprintf(s.Conn, "226 Directory listing complete.\r\n")
		return false

	case "LIST":
		raw, err := s.getRawDataConn()
		if err != nil {
			fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
			return false
		}
		fmt.Fprintf(s.Conn, "150 Opening ASCII mode data connection.\r\n")
		dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
		if err != nil {
			raw.Close()
			fmt.Fprintf(s.Conn, "435 Failed TLS negotiation on data channel\r\n")
			return false
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			var output strings.Builder
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				entries := bridge.ListDir(s.CurrentDir)
				now := time.Now().Format("Jan _2 15:04")
				siteName := s.Config.SiteNameShort
				if siteName == "" {
					siteName = "GoFTPd"
				}

				_, _, totalBytes, present, total := bridge.GetVFSRaceStats(s.CurrentDir)

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

					mode := "-rw-r--r--"
					size := fmt.Sprintf("%d", e.Size)
					if e.IsDir {
						mode = "drwxr-xr-x"
						size = "4096"
					}
					ts := time.Unix(e.ModTime, 0).Format("Jan _2 15:04")
					owner := "GoFTPd"
					group := "GoFTPd"
					output.WriteString(fmt.Sprintf("%s   1 %-8s %-8s %10s %s %s\r\n",
						mode, owner, group, size, ts, e.Name))
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
			dataConn.Write([]byte(output.String()))
			dataConn.Close()
			s.showGlobalStats("226", false)
			fmt.Fprintf(s.Conn, "226 Directory listing complete.\r\n")
			return false
		}

		dataConn.Close()
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

		raw, err := s.getRawDataConn()
		if err != nil {
			fmt.Fprintf(s.Conn, "425 Data connection failed\r\n")
			return false
		}
		fmt.Fprintf(s.Conn, "150 Opening binary mode data connection.\r\n")
		dataConn, err := s.upgradeDataTLS(raw, tlsConfig)
		if err != nil {
			raw.Close()
			return false
		}

		if s.Config.Mode == "master" && s.MasterManager != nil {
			if bridge, ok := s.MasterManager.(MasterBridge); ok {
				filePath := path.Join(s.CurrentDir, fileName)

				fileSize, checksum, err := bridge.UploadFile(filePath, dataConn, s.User.Name, s.User.PrimaryGroup)
				dataConn.Close()

				if err != nil {
					log.Printf("[MASTER] Upload failed: %v", err)
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

				fmt.Fprintf(s.Conn, "226 Transfer complete.\r\n")
			} else {
				fmt.Fprintf(s.Conn, "550 Master not initialized\r\n")
				dataConn.Close()
			}
			return false
		}

		dataConn.Close()
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
			} else {
				fmt.Fprintf(s.Conn, "550 Master not initialized\r\n")
			}
			return false
		}

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