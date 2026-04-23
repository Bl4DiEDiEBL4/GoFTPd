package core

import (
	"fmt"
	"path"
	"strings"
	"time"
)

const bwIdleThreshold = 60 * time.Second

func (s *Session) HandleSiteBW(args []string) bool {
	bridge, _ := s.MasterManager.(MasterBridge)
	slaveStats := collectLiveSlaveStats(bridge)

	if len(args) > 0 && strings.EqualFold(strings.TrimSpace(args[0]), "SLAVE") {
		s.handleSiteBWSlave(args[1:], slaveStats)
		return false
	}

	targetUser := ""
	if len(args) > 0 {
		targetUser = strings.TrimSpace(args[0])
	}

	snaps := listActiveSessions()
	if targetUser != "" {
		for _, snap := range snaps {
			if !strings.EqualFold(strings.TrimSpace(snap.User), targetUser) {
				continue
			}
			fmt.Fprintf(s.Conn, "200 %s\r\n", formatUserBandwidthLine(snap, slaveStats))
			return false
		}
		fmt.Fprintf(s.Conn, "550 User %s is not online.\r\n", targetUser)
		return false
	}

	var uploading, downloading, browsing, idling int
	var upSpeed, downSpeed float64
	for _, snap := range snaps {
		switch classifyBandwidthState(snap) {
		case "upload":
			uploading++
			if stat, ok := matchedSlaveStat(snap, slaveStats); ok {
				upSpeed += stat.SpeedBytes
			} else {
				upSpeed += snapshotTransferSpeedBytes(snap)
			}
		case "download":
			downloading++
			if stat, ok := matchedSlaveStat(snap, slaveStats); ok {
				downSpeed += stat.SpeedBytes
			} else {
				downSpeed += snapshotTransferSpeedBytes(snap)
			}
		case "browse":
			browsing++
		default:
			idling++
		}
	}

	// Include any unmatched live slave transfers so aggregate BW stays truthful.
	for _, stat := range slaveStats {
		if slaveStatMatched(stat, snaps) {
			continue
		}
		switch stat.Direction {
		case "upload":
			uploading++
			upSpeed += stat.SpeedBytes
		case "download":
			downloading++
			downSpeed += stat.SpeedBytes
		}
	}

	totalUsers := len(snaps)
	totalSpeed := upSpeed + downSpeed
	maxUsers := s.Config.MaxUsers
	if maxUsers <= 0 {
		maxUsers = totalUsers
	}

	fmt.Fprintf(s.Conn, "200 BANDWiDTH: (%d uploading at %s ~ 0%%) - (%d downloading at %s ~ 0%%) - (%d browsing) - (%d idling) - [%d out of %d users in total at %s ~ 0%%]\r\n",
		uploading, formatBandwidthSpeed(upSpeed),
		downloading, formatBandwidthSpeed(downSpeed),
		browsing, idling,
		totalUsers, maxUsers, formatBandwidthSpeed(totalSpeed))
	return false
}

func (s *Session) handleSiteBWSlave(args []string, slaveStats []LiveTransferStat) {
	targetSlave := ""
	if len(args) > 0 {
		targetSlave = strings.TrimSpace(args[0])
	}

	type slaveSummary struct {
		uploads   int
		downloads int
		upSpeed   float64
		downSpeed float64
	}
	summaries := map[string]*slaveSummary{}
	for _, stat := range slaveStats {
		if targetSlave != "" && !strings.EqualFold(stat.SlaveName, targetSlave) {
			continue
		}
		sum := summaries[stat.SlaveName]
		if sum == nil {
			sum = &slaveSummary{}
			summaries[stat.SlaveName] = sum
		}
		switch stat.Direction {
		case "upload":
			sum.uploads++
			sum.upSpeed += stat.SpeedBytes
		case "download":
			sum.downloads++
			sum.downSpeed += stat.SpeedBytes
		}
	}
	if targetSlave != "" && summaries[targetSlave] == nil {
		fmt.Fprintf(s.Conn, "550 Slave %s has no active transfers or is offline.\r\n", targetSlave)
		return
	}
	if len(summaries) == 0 {
		fmt.Fprintf(s.Conn, "200 BANDWiDTH: no active slave transfers.\r\n")
		return
	}

	for slaveName, sum := range summaries {
		total := sum.uploads + sum.downloads
		totalSpeed := sum.upSpeed + sum.downSpeed
		fmt.Fprintf(s.Conn, "200 BANDWiDTH: %s - (%d uploading at %s) - (%d downloading at %s) - [%d transfers at %s]\r\n",
			slaveName,
			sum.uploads, formatBandwidthSpeed(sum.upSpeed),
			sum.downloads, formatBandwidthSpeed(sum.downSpeed),
			total, formatBandwidthSpeed(totalSpeed))
	}
}

func classifyBandwidthState(snap sessionSnapshot) string {
	switch snap.TransferDirection {
	case "upload":
		return "upload"
	case "download":
		return "download"
	}
	last := snap.LastCommandAt
	if last.IsZero() {
		last = snap.StartedAt
	}
	if time.Since(last) > bwIdleThreshold {
		return "idle"
	}
	return "browse"
}

func snapshotTransferSpeedBytes(snap sessionSnapshot) float64 {
	if snap.TransferDirection == "" || snap.TransferStartedAt.IsZero() || snap.TransferBytes <= 0 {
		return 0
	}
	seconds := time.Since(snap.TransferStartedAt).Seconds()
	if seconds <= 0 {
		return 0
	}
	return float64(snap.TransferBytes) / seconds
}

func formatBandwidthSpeed(bytesPerSecond float64) string {
	if bytesPerSecond <= 0 {
		return "0.0KB/s"
	}
	return fmt.Sprintf("%.1fKB/s", bytesPerSecond/1024.0)
}

func formatUserBandwidthLine(snap sessionSnapshot, slaveStats []LiveTransferStat) string {
	user := snap.User
	if user == "" {
		user = "(login)"
	}
	if stat, ok := matchedSlaveStat(snap, slaveStats); ok {
		switch classifyBandwidthState(snap) {
		case "upload":
			return fmt.Sprintf("BANDWiDTH: %s is uploading %s at %s via %s", user, pathBaseOrUnknown(stat.Path), formatBandwidthSpeed(stat.SpeedBytes), stat.SlaveName)
		case "download":
			return fmt.Sprintf("BANDWiDTH: %s is downloading %s at %s via %s", user, pathBaseOrUnknown(stat.Path), formatBandwidthSpeed(stat.SpeedBytes), stat.SlaveName)
		}
	}
	switch classifyBandwidthState(snap) {
	case "upload":
		return fmt.Sprintf("BANDWiDTH: %s is uploading %s at %s", user, pathBaseOrUnknown(snap.TransferPath), formatBandwidthSpeed(snapshotTransferSpeedBytes(snap)))
	case "download":
		return fmt.Sprintf("BANDWiDTH: %s is downloading %s at %s", user, pathBaseOrUnknown(snap.TransferPath), formatBandwidthSpeed(snapshotTransferSpeedBytes(snap)))
	case "browse":
		return fmt.Sprintf("BANDWiDTH: %s is browsing %s", user, snap.CurrentDir)
	default:
		last := snap.LastCommandAt
		if last.IsZero() {
			last = snap.StartedAt
		}
		return fmt.Sprintf("BANDWiDTH: %s has been idle for %s in %s", user, time.Since(last).Round(time.Second), snap.CurrentDir)
	}
}

func collectLiveSlaveStats(bridge MasterBridge) []LiveTransferStat {
	if bridge == nil {
		return nil
	}
	return bridge.GetLiveTransferStats()
}

func matchedSlaveStat(snap sessionSnapshot, slaveStats []LiveTransferStat) (LiveTransferStat, bool) {
	if snap.TransferSlaveName == "" || snap.TransferSlaveIdx == 0 {
		return LiveTransferStat{}, false
	}
	for _, stat := range slaveStats {
		if !strings.EqualFold(stat.SlaveName, snap.TransferSlaveName) {
			continue
		}
		if stat.TransferIndex != snap.TransferSlaveIdx {
			continue
		}
		return stat, true
	}
	return LiveTransferStat{}, false
}

func slaveStatMatched(stat LiveTransferStat, snaps []sessionSnapshot) bool {
	for _, snap := range snaps {
		if snap.TransferSlaveName == "" || snap.TransferSlaveIdx == 0 {
			continue
		}
		if strings.EqualFold(stat.SlaveName, snap.TransferSlaveName) && stat.TransferIndex == snap.TransferSlaveIdx {
			return true
		}
	}
	return false
}

func pathBaseOrUnknown(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "(unknown)"
	}
	base := path.Base(strings.TrimRight(p, "/"))
	if base == "." || base == "/" || base == "" {
		return p
	}
	return base
}
