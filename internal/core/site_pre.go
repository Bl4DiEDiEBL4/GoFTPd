package core

import (
	"fmt"
	"log"
	"path"
	"strings"
	"time"
)

// HandleSitePre handles SITE PRE <releasename> <section>
//
// Semantics (modelled after glftpd/foo-tools pre):
//   - Caller must be a member of at least one affil group with a predir set.
//   - Release directory must exist under that group's predir.
//   - Section must be in pre_sections whitelist.
//   - Release is moved from /PRE/<group>/<rel> -> /<section>/<rel>.
//   - A PRE event is emitted for sitebot announce.
//   - An async bandwidth sampler polls VFS race stats every pre_bw_interval_ms
//     for pre_bw_duration seconds, then emits PREBW / PREBWINTERVAL / PREBWUSER.
func (s *Session) HandleSitePre(args []string) bool {
	if !s.Config.PreEnabled {
		fmt.Fprintf(s.Conn, "502 SITE PRE disabled on this site.\r\n")
		return false
	}
	if len(args) < 2 {
		fmt.Fprintf(s.Conn, "501 Usage: SITE PRE <releasename> <section>\r\n")
		return false
	}
	relname := strings.TrimSpace(args[0])
	section := strings.TrimSpace(args[1])
	if relname == "" || strings.ContainsAny(relname, "/\\") {
		fmt.Fprintf(s.Conn, "501 Invalid release name.\r\n")
		return false
	}
	if !preSectionAllowed(s.Config.PreSections, section) {
		fmt.Fprintf(s.Conn, "501 Section %q is not a valid pre section.\r\n", section)
		return false
	}

	// Find affil group the user belongs to that has a predir configured.
	affil := s.findUserAffil()
	if affil == nil {
		fmt.Fprintf(s.Conn, "550 You are not in any affil group.\r\n")
		return false
	}

	// Construct paths.
	// Source: <predir>/<relname>  (predir comes straight from affil rule)
	src := path.Join(affil.Predir, relname)

	// Destination: /<section>/<relname>, or /<section>/<MMDD>/<relname> for
	// dated sections (MP3/FLAC/0DAY typically). MMDD is today's date —
	// matches the scene convention where /MP3/0419/ holds today's releases.
	destSection := "/" + section
	if preSectionIsDated(s.Config.PreDatedSections, section) {
		destSection = path.Join(destSection, time.Now().Format("0102"))
	}
	dst := path.Join(destSection, relname)

	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok || bridge == nil {
		fmt.Fprintf(s.Conn, "451 Master bridge unavailable.\r\n")
		return false
	}

	// Verify source exists.
	entries := bridge.ListDir(affil.Predir)
	found := false
	for _, e := range entries {
		if e.IsDir && e.Name == relname {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(s.Conn, "550 Release %q not found in %s.\r\n", relname, affil.Predir)
		return false
	}

	// Stats pre-move (files + bytes) via race DB / VFS.
	_, _, totalBytes, present, _ := bridge.GetVFSRaceStats(src)
	mbytes := float64(totalBytes) / 1024.0 / 1024.0

	// For dated sections, verify the MMDD subdir exists before we try to
	// rename into it. A separate midnight script is expected to pre-create
	// /<section>/<MMDD>/ dirs; if it hasn't run we create it ourselves.
	if destSection != "/"+section {
		exists := false
		for _, e := range bridge.ListDir("/" + section) {
			if e.IsDir && e.Name == path.Base(destSection) {
				exists = true
				break
			}
		}
		if !exists {
			bridge.MakeDir(destSection, s.User.Name, s.User.PrimaryGroup)
		}
	}

	// Perform the move (rename on slaves + VFS + race DB path update).
	bridge.RenameFile(src, destSection, relname)

	log.Printf("[PRE] %s pre'd %s to %s (%d files, %.0f MB)",
		s.User.Name, relname, dst, present, mbytes)

	// Emit PRE event for sitebot announce.
	s.emitEvent(EventPre, dst, relname, totalBytes, 0, map[string]string{
		"relname":  relname,
		"section":  section,
		"group":    affil.Group,
		"user":     s.User.Name,
		"t_files":  fmt.Sprintf("%d", present),
		"t_mbytes": fmt.Sprintf("%.0fMB", mbytes),
	})

	// Kick off async bandwidth sampler. This mirrors fin-prebw.py's behavior:
	// polls race stats periodically, tracks per-user bps, computes avg/peak
	// and interval snapshots, emits PREBW+PREBWUSER+PREBWINTERVAL when done.
	go runPreBWSampler(s, bridge, dst, relname, section, affil.Group)

	fmt.Fprintf(s.Conn, "200 %s pre'd to %s successfully.\r\n", relname, dst)
	return false
}

// findUserAffil returns the first AffilRule where the user's groups contain
// the rule's Group. Returns nil if the user has no affil.
func (s *Session) findUserAffil() *AffilRule {
	if s.User == nil {
		return nil
	}
	userGroups := map[string]bool{}
	userGroups[strings.ToLower(s.User.PrimaryGroup)] = true
	for g := range s.User.Groups {
		userGroups[strings.ToLower(g)] = true
	}
	for i := range s.Config.Affils {
		a := &s.Config.Affils[i]
		if a.Group == "" || a.Predir == "" {
			continue
		}
		if userGroups[strings.ToLower(a.Group)] {
			return a
		}
	}
	return nil
}

func preSectionAllowed(allowed []string, section string) bool {
	if len(allowed) == 0 {
		return true // no whitelist = anything goes
	}
	up := strings.ToUpper(section)
	for _, s := range allowed {
		if strings.EqualFold(up, s) {
			return true
		}
	}
	return false
}

// preSectionIsDated returns true if releases pre'd to this section should
// go into a /<section>/<MMDD>/ subdir (scene convention for MP3/FLAC/0DAY).
// Matched case-insensitively against the configured list.
func preSectionIsDated(dated []string, section string) bool {
	up := strings.ToUpper(section)
	for _, s := range dated {
		if strings.EqualFold(up, s) {
			return true
		}
	}
	return false
}

// =============================================================================
// Bandwidth sampler — Go port of fin-prebw.py
// =============================================================================

// userSnapshot captures one user's total bytes at a moment in time.
type userSnapshot struct {
	Bytes int64
	Files int
}

// runPreBWSampler polls the VFS race stats periodically for dst, tracks
// per-user deltas to compute bytes/sec, then emits summary events.
// Blocks until the sample duration elapses or the race goes idle.
func runPreBWSampler(s *Session, bridge MasterBridge, dst, relname, section, group string) {
	duration := s.Config.PreBWDuration
	if duration <= 0 {
		duration = 30
	}
	intervalMs := s.Config.PreBWIntervalMs
	if intervalMs <= 0 {
		intervalMs = 500
	}
	poll := time.Duration(intervalMs) * time.Millisecond
	slots := (duration * 1000) / intervalMs
	if slots < 1 {
		slots = 1
	}

	// Snapshot intervals in seconds, matching fin-prebw.py CAPS [2,3,5,10,10]
	// (cumulative: 2s, 5s, 10s, 20s, 30s)
	caps := []int{2, 3, 5, 10, 10}

	// Per-slot total bps, across all users
	perSec := make([]int64, slots+1)
	// Per-user peak bps + cumulative bytes at end + last-seen bytes (for delta)
	type userAgg struct {
		lastBytes int64
		peakBps   int64
		sumBps    int64
		samples   int
		bytes     int64
		files     int
	}
	userAggs := map[string]*userAgg{}

	// Prior snapshot per user for computing delta
	prev := map[string]userSnapshot{}

	idleSlots := 0
	const idleBreak = 20 // seconds — match fin-prebw.py IDLE_BREAK_SEC

	for slot := 1; slot <= slots; slot++ {
		time.Sleep(poll)

		users, _, totalBytes, present, _ := bridge.GetVFSRaceStats(dst)
		_ = totalBytes
		_ = present

		slotTotalBps := int64(0)
		anyActivity := false

		for _, u := range users {
			cur := userSnapshot{Bytes: u.Bytes, Files: u.Files}
			p, had := prev[u.Name]
			var deltaBytes int64
			if had {
				deltaBytes = cur.Bytes - p.Bytes
				if deltaBytes < 0 {
					deltaBytes = 0
				}
			}
			prev[u.Name] = cur

			// bytes/sec = delta / interval
			bps := int64(float64(deltaBytes) * 1000.0 / float64(intervalMs))
			if bps > 0 {
				anyActivity = true
			}
			slotTotalBps += bps

			agg := userAggs[u.Name]
			if agg == nil {
				agg = &userAgg{}
				userAggs[u.Name] = agg
			}
			if bps > agg.peakBps {
				agg.peakBps = bps
			}
			agg.sumBps += bps
			agg.samples++
			agg.bytes = u.Bytes
			agg.files = u.Files
		}

		perSec[slot] = slotTotalBps

		if !anyActivity {
			idleSlots++
			idleSlotsSec := idleSlots * intervalMs / 1000
			if idleSlotsSec >= idleBreak {
				perSec = perSec[:slot+1]
				break
			}
		} else {
			idleSlots = 0
		}
	}

	// Compute totals
	actualSlots := len(perSec) - 1
	if actualSlots < 1 {
		actualSlots = 1
	}
	var sum, peak int64
	for i := 1; i <= actualSlots && i < len(perSec); i++ {
		sum += perSec[i]
		if perSec[i] > peak {
			peak = perSec[i]
		}
	}
	var avg int64
	if actualSlots > 0 {
		avg = sum / int64(actualSlots)
	}

	// Interval snapshots at cumulative CAPS
	intervalSnaps := make([][2]interface{}, 0, len(caps))
	cum := 0
	for _, cap := range caps {
		cum += cap
		idx := (cum * 1000) / intervalMs
		if idx > actualSlots {
			idx = actualSlots
		}
		bps := int64(0)
		if idx < len(perSec) {
			bps = perSec[idx]
		}
		intervalSnaps = append(intervalSnaps, [2]interface{}{bps, cum})
	}

	// Totals for the PREBW line
	var grandBytes int64
	for _, u := range userAggs {
		grandBytes += u.bytes
	}

	// Emit PREBW (totals)
	trafV, trafU := fmtSize(grandBytes)
	avgV, avgU := fmtBps(avg)
	peakV, peakU := fmtBps(peak)
	s.emitEvent(EventPreBW, dst, relname, grandBytes, 0, map[string]string{
		"relname":      relname,
		"section":      section,
		"group":        group,
		"traffic_val":  trafV,
		"traffic_unit": trafU,
		"avg_val":      avgV,
		"avg_unit":     avgU,
		"peak_val":     peakV,
		"peak_unit":    peakU,
	})

	// Emit PREBWINTERVAL
	intervalData := map[string]string{
		"relname":   relname,
		"section":   section,
		"group":     group,
		"peak_val":  peakV,
		"peak_unit": peakU,
	}
	for i, snap := range intervalSnaps {
		bps := snap[0].(int64)
		tm := snap[1].(int)
		v, u := fmtBps(bps)
		intervalData[fmt.Sprintf("b%d", i+1)] = v
		intervalData[fmt.Sprintf("u%d", i+1)] = u
		intervalData[fmt.Sprintf("t%d", i+1)] = fmt.Sprintf("%d", tm)
	}
	s.emitEvent(EventPreBWInterval, dst, relname, 0, 0, intervalData)

	// Emit PREBWUSER per user, sorted by total bytes desc
	type userRow struct {
		name  string
		bytes int64
		files int
		avg   int64
		peak  int64
	}
	rows := make([]userRow, 0, len(userAggs))
	for name, a := range userAggs {
		if a.files == 0 {
			continue
		}
		avgU := int64(0)
		if a.samples > 0 {
			avgU = a.sumBps / int64(a.samples)
		}
		rows = append(rows, userRow{
			name: name, bytes: a.bytes, files: a.files, avg: avgU, peak: a.peakBps,
		})
	}
	// sort desc by bytes
	for i := 0; i < len(rows); i++ {
		for j := i + 1; j < len(rows); j++ {
			if rows[j].bytes > rows[i].bytes {
				rows[i], rows[j] = rows[j], rows[i]
			}
		}
	}
	for _, r := range rows {
		sv, su := fmtSize(r.bytes)
		avgV, avgU := fmtBps(r.avg)
		peakV, peakU := fmtBps(r.peak)
		s.emitEvent(EventPreBWUser, dst, relname, r.bytes, 0, map[string]string{
			"relname":        relname,
			"section":        section,
			"group":          group,
			"user":           r.name,
			"size_val":       sv,
			"size_unit":      su,
			"cnt_files":      fmt.Sprintf("%d", r.files),
			"avg_val_user":   avgV,
			"avg_unit_user":  avgU,
			"peak_val_user":  peakV,
			"peak_unit_user": peakU,
		})
	}
}

// fmtSize formats bytes as (value, unit) string — GB or MB.
func fmtSize(b int64) (string, string) {
	if b >= 1<<30 {
		return fmt.Sprintf("%.2f", float64(b)/float64(1<<30)), "GB"
	}
	return fmt.Sprintf("%.1f", float64(b)/float64(1<<20)), "MB"
}

// fmtBps formats bytes/sec as (value, unit) — GB/s or MB/s.
func fmtBps(bps int64) (string, string) {
	mb := float64(bps) / float64(1<<20)
	if mb >= 1024 {
		return fmt.Sprintf("%.1f", mb/1024.0), "GB/s"
	}
	return fmt.Sprintf("%.1f", mb), "MB/s"
}
