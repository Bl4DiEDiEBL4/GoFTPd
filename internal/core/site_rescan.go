package core

import (
	"fmt"
	"path"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"goftpd/internal/zipscript"
)

type rescanOptions struct {
	Target         string
	Recursive      bool
	ForceRescan    bool
	DeleteBad      bool
	DeleteZeroByte bool
	Quiet          bool
}

type rescanReleaseResult struct {
	Path         string
	SFV          string
	Total        int
	OK           int
	Missing      int
	Bad          int
	ZipChecked   int
	ZipBad       int
	DIZRecovered bool
	MissingFiles []string
	BadFiles     []string
	OfflineFiles []string
	FailedFiles  []string
	BadZips      []string
	Errors       []string
}

type sfvReconcileResult struct {
	OK           int
	Missing      int
	Bad          int
	MissingFiles []string
	BadFiles     []string
	OfflineFiles []string
	FailedFiles  []string
	Errors       []string
}

type zipRescanResult struct {
	Checked      int
	Bad          int
	DIZRecovered bool
	BadFiles     []string
	Errors       []string
}

type rescanJob struct {
	ID             string
	User           string
	Target         string
	CreatedAt      time.Time
	StartedAt      time.Time
	CompletedAt    time.Time
	Status         string
	TotalItems     int
	CompletedItems int
	lines          []string
	lastReadByUser map[string]int
	mu             sync.Mutex
}

type rescanJobManager struct {
	seq        uint64
	mu         sync.RWMutex
	jobs       map[string]*rescanJob
	lastByUser map[string]string
}

var globalRescanJobs = &rescanJobManager{
	jobs:       make(map[string]*rescanJob),
	lastByUser: make(map[string]string),
}

func (m *rescanJobManager) Create(user, target string, totalItems int) *rescanJob {
	id := fmt.Sprintf("R%06d", atomic.AddUint64(&m.seq, 1))
	job := &rescanJob{
		ID:             id,
		User:           user,
		Target:         target,
		CreatedAt:      time.Now(),
		Status:         "queued",
		TotalItems:     totalItems,
		lastReadByUser: make(map[string]int),
	}
	m.mu.Lock()
	m.jobs[id] = job
	if strings.TrimSpace(user) != "" {
		m.lastByUser[strings.ToLower(strings.TrimSpace(user))] = id
	}
	m.mu.Unlock()
	return job
}

func (m *rescanJobManager) Find(id, user string) *rescanJob {
	id = strings.TrimSpace(id)
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id == "" {
		id = m.lastByUser[strings.ToLower(strings.TrimSpace(user))]
	}
	return m.jobs[id]
}

func (j *rescanJob) Start() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = "running"
	j.StartedAt = time.Now()
}

func (j *rescanJob) Complete() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = "done"
	j.CompletedAt = time.Now()
}

func (j *rescanJob) CurrentStatus() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.Status
}

func (j *rescanJob) Fail(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = "failed"
	j.CompletedAt = time.Now()
	if err != nil {
		j.lines = append(j.lines, "ERROR: "+strings.TrimSpace(err.Error()))
	}
}

func (j *rescanJob) Advance() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.CompletedItems++
}

func (j *rescanJob) Append(line string) {
	line = strings.TrimSpace(line)
	if line == "" {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.lines = append(j.lines, line)
}

func (j *rescanJob) SnapshotUnread(user string) (status string, target string, totalItems, completedItems int, createdAt, startedAt, completedAt time.Time, lines []string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	status = j.Status
	target = j.Target
	totalItems = j.TotalItems
	completedItems = j.CompletedItems
	createdAt = j.CreatedAt
	startedAt = j.StartedAt
	completedAt = j.CompletedAt
	key := strings.ToLower(strings.TrimSpace(user))
	from := j.lastReadByUser[key]
	if from < 0 || from > len(j.lines) {
		from = 0
	}
	lines = append([]string(nil), j.lines[from:]...)
	j.lastReadByUser[key] = len(j.lines)
	return
}

func (s *Session) HandleSiteRescan(args []string) bool {
	opts, err := parseRescanOptions(s.CurrentDir, args)
	if err != nil {
		fmt.Fprintf(s.Conn, "501 %v\r\n", err)
		return false
	}
	if s.Config.Mode != "master" || s.MasterManager == nil {
		fmt.Fprintf(s.Conn, "550 SITE RESCAN is only available in master mode.\r\n")
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok {
		fmt.Fprintf(s.Conn, "550 Master not initialized.\r\n")
		return false
	}

	aclTarget := strings.TrimSuffix(opts.Target, "/*")
	if s.ACLEngine != nil && !s.ACLEngine.CanPerform(s.User, "RESCAN", path.Join(s.Config.ACLBasePath, aclTarget)) {
		fmt.Fprintf(s.Conn, "550 Access Denied: Insufficient flags.\r\n")
		return false
	}

	targetEntry, exists := bridge.GetPathEntry(opts.Target)
	if !exists && !strings.HasSuffix(opts.Target, "/*") {
		fmt.Fprintf(s.Conn, "550 %s is not a valid file or directory\r\n", opts.Target)
		return false
	}
	if targetEntry.IsDir && strings.HasSuffix(strings.TrimSpace(path.Base(opts.Target)), "/*") {
		fmt.Fprintf(s.Conn, "550 %s is not a valid file or directory\r\n", opts.Target)
		return false
	}
	if opts.Recursive && exists && !targetEntry.IsDir {
		fmt.Fprintf(s.Conn, "550 Not possible to recursively work on a single file\r\n")
		return false
	}

	if exists && !targetEntry.IsDir {
		job := globalRescanJobs.Create(s.User.Name, opts.Target, 1)
		go s.runRescanJob(job, bridge, []string{opts.Target}, opts, true)
		fmt.Fprintf(s.Conn, "200- Rescan queued as %s for %s\r\n", job.ID, opts.Target)
		fmt.Fprintf(s.Conn, "200- Use SITE RESCANSTATUS %s to view progress/output\r\n", job.ID)
		fmt.Fprintf(s.Conn, "200 Rescan running in background.\r\n")
		return false
	}

	releases := expandRescanTargets(bridge, opts.Target, opts.Recursive)
	if len(releases) == 0 {
		fmt.Fprintf(s.Conn, "550 No matching release directories for %s\r\n", opts.Target)
		return false
	}

	job := globalRescanJobs.Create(s.User.Name, opts.Target, len(releases))
	go s.runRescanJob(job, bridge, releases, opts, false)
	fmt.Fprintf(s.Conn, "200- Rescan queued as %s for %s\r\n", job.ID, opts.Target)
	fmt.Fprintf(s.Conn, "200- Use SITE RESCANSTATUS %s to view progress/output\r\n", job.ID)
	fmt.Fprintf(s.Conn, "200 Rescan running in background.\r\n")
	return false
}

func (s *Session) HandleSiteRescanStatus(args []string) bool {
	jobID := ""
	if len(args) > 0 {
		jobID = strings.TrimSpace(args[0])
	}
	job := globalRescanJobs.Find(jobID, s.User.Name)
	if job == nil {
		fmt.Fprintf(s.Conn, "550 No matching rescan job.\r\n")
		return false
	}
	status, target, totalItems, completedItems, createdAt, startedAt, completedAt, lines := job.SnapshotUnread(s.User.Name)
	fmt.Fprintf(s.Conn, "200- RESCAN %s status=%s target=%s progress=%d/%d created=%s\r\n",
		job.ID, status, target, completedItems, totalItems, formatUnixTime(createdAt.Unix()))
	if !startedAt.IsZero() {
		fmt.Fprintf(s.Conn, "200- Started: %s\r\n", formatUnixTime(startedAt.Unix()))
	}
	if !completedAt.IsZero() {
		fmt.Fprintf(s.Conn, "200- Finished: %s\r\n", formatUnixTime(completedAt.Unix()))
	}
	if len(lines) == 0 {
		fmt.Fprintf(s.Conn, "200- No new output.\r\n")
	} else {
		for _, line := range lines {
			fmt.Fprintf(s.Conn, "200- %s\r\n", line)
		}
	}
	fmt.Fprintf(s.Conn, "200 End of RESCANSTATUS\r\n")
	return false
}

func (s *Session) runRescanJob(job *rescanJob, bridge MasterBridge, targets []string, opts rescanOptions, singleFile bool) {
	job.Start()
	job.Append(fmt.Sprintf("Starting zipscript rescan for %s", job.Target))
	defer func() {
		if r := recover(); r != nil {
			job.Fail(fmt.Errorf("panic: %v", r))
			return
		}
		if job.CurrentStatus() != "failed" {
			if singleFile {
				job.Append("Rescan complete: 1 file checked.")
			} else if len(targets) == 1 {
				job.Append("Rescan complete: 1 release checked.")
			} else {
				job.Append(fmt.Sprintf("Rescan complete: %d releases checked.", len(targets)))
			}
			job.Complete()
		}
	}()

	for i, target := range targets {
		if !opts.Quiet {
			job.Append(fmt.Sprintf("[%d/%d] Scanning %s", i+1, len(targets), target))
		}
		var result rescanReleaseResult
		if singleFile {
			result = s.rescanSingleFile(bridge, target, opts, job.Append)
		} else {
			result = s.rescanRelease(bridge, target, opts, job.Append)
		}
		for _, line := range rescanResultLines(result) {
			job.Append(line)
		}
		job.Advance()
	}
}

func parseRescanOptions(currentDir string, args []string) (rescanOptions, error) {
	opts := rescanOptions{
		ForceRescan:    true,
		DeleteBad:      true,
		DeleteZeroByte: true,
	}

	var target string
	for _, raw := range args {
		arg := strings.TrimSpace(raw)
		switch strings.ToLower(arg) {
		case "-r":
			opts.Recursive = true
		case "noforce":
			opts.ForceRescan = false
		case "nodelete":
			opts.DeleteBad = false
		case "nodelete0byte":
			opts.DeleteZeroByte = false
		case "quiet":
			opts.Quiet = true
		case "":
			// ignore
		default:
			if target != "" {
				return opts, fmt.Errorf("Usage: SITE RESCAN [path|path/*] [-r] [noforce] [nodelete] [nodelete0byte] [quiet]")
			}
			target = arg
		}
	}
	if strings.TrimSpace(target) == "" {
		target = currentDir
	}
	if !opts.DeleteBad && !opts.DeleteZeroByte {
		opts.ForceRescan = false
	}
	opts.Target = resolveSitePath(currentDir, target)
	return opts, nil
}

func (s *Session) rescanSingleFile(bridge MasterBridge, filePath string, opts rescanOptions, report func(string)) rescanReleaseResult {
	result := rescanReleaseResult{Path: filePath}
	releasePath := path.Dir(filePath)
	fileName := path.Base(filePath)

	sfvName, ok := findSFV(bridge, releasePath)
	if !ok {
		result.Errors = append(result.Errors, "no SFV found")
		result.Errors = append(result.Errors, "unable to obtain SFVInfo, cannot do anything")
		return result
	}
	result.SFV = sfvName

	sfvPath := path.Join(releasePath, sfvName)
	sfvInfo, err := bridge.GetSFVInfo(sfvPath)
	if err != nil {
		result.Errors = append(result.Errors, err.Error())
		return result
	}
	entries := sfvInfo.Entries
	bridge.CacheSFV(releasePath, sfvName, sfvInfo)
	result.Total = len(entries)

	var matched *SFVEntryInfo
	for i := range entries {
		if strings.EqualFold(entries[i].FileName, fileName) {
			matched = &entries[i]
			break
		}
	}
	if matched == nil {
		result.Errors = append(result.Errors, fmt.Sprintf("File (%s) does not exist in sfv", fileName))
		return result
	}

	reconcile := reconcileSingleSFVEntry(bridge, releasePath, *matched, opts, report)
	result.OK = reconcile.OK
	result.Missing = reconcile.Missing
	result.Bad = reconcile.Bad
	result.MissingFiles = reconcile.MissingFiles
	result.BadFiles = reconcile.BadFiles
	result.OfflineFiles = reconcile.OfflineFiles
	result.FailedFiles = reconcile.FailedFiles
	result.Errors = append(result.Errors, reconcile.Errors...)

	zipResult := rescanZipPostProcess(s.Config, bridge, releasePath, opts)
	result.ZipChecked = zipResult.Checked
	result.ZipBad = zipResult.Bad
	result.DIZRecovered = zipResult.DIZRecovered
	result.BadZips = zipResult.BadFiles
	result.Errors = append(result.Errors, zipResult.Errors...)

	if shouldRefreshRescanMediaInfo(s.Config, releasePath) {
		if _, fields, ok := findFirstUsableAudioInfo(bridge, s.Config, releasePath); ok {
			previousFields := cloneStringMap(bridge.GetDirMediaInfo(releasePath))
			bridge.CacheMediaInfo(releasePath, fields)
			if err := refreshAudioSortLinks(bridge, s.Config.Zipscript, releasePath, previousFields, fields); err != nil {
				if s.Config != nil && s.Config.Debug {
					result.Errors = append(result.Errors, fmt.Sprintf("audio sort refresh skipped: %v", err))
				}
			}
		}
	}
	bridge.SyncStatusMarkersForPath(releasePath, true)
	return result
}

func (s *Session) rescanRelease(bridge MasterBridge, releasePath string, opts rescanOptions, report func(string)) rescanReleaseResult {
	result := rescanReleaseResult{Path: releasePath}
	sfvName, ok := findSFV(bridge, releasePath)
	if ok {
		result.SFV = sfvName

		sfvPath := path.Join(releasePath, sfvName)
		sfvInfo, err := bridge.GetSFVInfo(sfvPath)
		if err != nil {
			result.Errors = append(result.Errors, err.Error())
		} else {
			entries := sfvInfo.Entries
			bridge.CacheSFV(releasePath, sfvName, sfvInfo)
			result.Total = len(entries)
			reconcile := reconcileReleaseSFVEntries(bridge, releasePath, entries, opts, report)
			result.OK = reconcile.OK
			result.Missing = reconcile.Missing
			result.Bad = reconcile.Bad
			result.MissingFiles = reconcile.MissingFiles
			result.BadFiles = reconcile.BadFiles
			result.OfflineFiles = reconcile.OfflineFiles
			result.FailedFiles = reconcile.FailedFiles
			result.Errors = append(result.Errors, reconcile.Errors...)
		}
	} else {
		result.Errors = append(result.Errors, "no SFV found")
	}

	zipResult := rescanZipPostProcess(s.Config, bridge, releasePath, opts)
	result.ZipChecked = zipResult.Checked
	result.ZipBad = zipResult.Bad
	result.DIZRecovered = zipResult.DIZRecovered
	result.BadZips = zipResult.BadFiles
	result.Errors = append(result.Errors, zipResult.Errors...)

	if shouldRefreshRescanMediaInfo(s.Config, releasePath) {
		if _, fields, ok := findFirstUsableAudioInfo(bridge, s.Config, releasePath); ok {
			previousFields := cloneStringMap(bridge.GetDirMediaInfo(releasePath))
			bridge.CacheMediaInfo(releasePath, fields)
			if err := refreshAudioSortLinks(bridge, s.Config.Zipscript, releasePath, previousFields, fields); err != nil {
				if s.Config != nil && s.Config.Debug {
					result.Errors = append(result.Errors, fmt.Sprintf("audio sort refresh skipped: %v", err))
				}
			}
		}
	}
	bridge.SyncStatusMarkersForPath(releasePath, true)

	return result
}

func reconcileSingleSFVEntry(bridge MasterBridge, releasePath string, entry SFVEntryInfo, opts rescanOptions, report func(string)) sfvReconcileResult {
	return reconcileReleaseSFVEntries(bridge, releasePath, []SFVEntryInfo{entry}, opts, report)
}

func reconcileReleaseSFVEntries(bridge MasterBridge, releasePath string, entries []SFVEntryInfo, opts rescanOptions, report func(string)) sfvReconcileResult {
	result := sfvReconcileResult{}
	for _, entry := range entries {
		filePath := path.Join(releasePath, entry.FileName)
		missingPath := filePath + "-MISSING"

		if bridge.GetFileSize(filePath) < 0 {
			result.Missing++
			result.MissingFiles = append(result.MissingFiles, entry.FileName)
			if report != nil {
				report("MISSING: " + entry.FileName)
			}
			_ = bridge.MarkFileMissing(filePath)
			if err := bridge.WriteFile(missingPath, []byte{}); err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s missing marker failed: %v", entry.FileName, err))
			}
			continue
		}

		checksum, err := checksumForRescan(bridge, filePath, opts.ForceRescan)
		if err != nil {
			switch {
			case isRescanMissingError(err):
				result.Missing++
				result.MissingFiles = append(result.MissingFiles, entry.FileName)
				if report != nil {
					report("MISSING: " + entry.FileName)
				}
				_ = bridge.MarkFileMissing(filePath)
				if err := bridge.WriteFile(missingPath, []byte{}); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s missing marker failed: %v", entry.FileName, err))
				}
			case isRescanOfflineError(err):
				result.OfflineFiles = append(result.OfflineFiles, entry.FileName)
				if report != nil {
					report("OFFLINE: " + entry.FileName)
				}
			default:
				result.FailedFiles = append(result.FailedFiles, entry.FileName)
				if report != nil {
					report("CHECKSUM FAILED: " + entry.FileName)
				}
				if !opts.Quiet {
					result.Errors = append(result.Errors, fmt.Sprintf("%s (FAILED - failed to checksum file)", entry.FileName))
				}
			}
			continue
		}
		if checksum != entry.CRC32 {
			if checksum == 0 && bridge.GetFileSize(filePath) == 0 && !opts.DeleteZeroByte {
				result.Bad++
				result.BadFiles = append(result.BadFiles, entry.FileName)
				if !opts.Quiet {
					result.Errors = append(result.Errors, fmt.Sprintf("%s (ZEROBYTE) SFV: %08X SLAVE: %08X", entry.FileName, entry.CRC32, checksum))
				}
				continue
			}
			if checksum == 0 && bridge.GetFileSize(filePath) > 0 {
				result.FailedFiles = append(result.FailedFiles, entry.FileName)
				if !opts.Quiet {
					result.Errors = append(result.Errors, fmt.Sprintf("%s (FAILED - failed to checksum file) SFV: %08X SLAVE: %08X", entry.FileName, entry.CRC32, checksum))
				}
				continue
			}
			result.Bad++
			result.BadFiles = append(result.BadFiles, entry.FileName)
			if report != nil {
				report(fmt.Sprintf("BAD: %s SFV:%08X SLAVE:%08X", entry.FileName, entry.CRC32, checksum))
			}
			if opts.DeleteBad || (checksum == 0 && opts.DeleteZeroByte) {
				if err := bridge.DeleteFile(filePath); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s delete failed: %v", entry.FileName, err))
				}
				_ = bridge.MarkFileMissing(filePath)
				if err := bridge.WriteFile(missingPath, []byte{}); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s missing marker failed: %v", entry.FileName, err))
				}
			}
			continue
		}

		result.OK++
		if report != nil && !opts.Quiet {
			report(fmt.Sprintf("OK: %s CRC:%08X", entry.FileName, checksum))
		}
		_ = bridge.SyncPresentFile(filePath, checksum)
		if bridge.GetFileSize(missingPath) >= 0 {
			_ = bridge.DeleteFile(missingPath)
		}
	}
	return result
}

func checksumForRescan(bridge MasterBridge, filePath string, force bool) (uint32, error) {
	if !force {
		if checksum, ok := bridge.GetKnownChecksum(filePath); ok {
			return checksum, nil
		}
	}
	return bridge.ChecksumFile(filePath)
}

func isRescanMissingError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(strings.TrimSpace(err.Error())), "file not found")
}

func isRescanOfflineError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(strings.TrimSpace(err.Error()))
	for _, needle := range []string{
		"offline",
		"no available slave",
		"not online",
		"connection reset",
		"i/o timeout",
		"tls",
		"eof",
	} {
		if strings.Contains(lower, needle) {
			return true
		}
	}
	return false
}

func rescanResultLines(result rescanReleaseResult) []string {
	lines := []string{fmt.Sprintf("Rescanning %s", result.Path)}
	if result.SFV != "" {
		lines = append(lines, fmt.Sprintf("SFV: %s (%d files)", result.SFV, result.Total))
	}
	for _, errText := range result.Errors {
		lines = append(lines, "ERROR: "+errText)
	}
	if result.SFV != "" {
		lines = append(lines, fmt.Sprintf("OK: %d Missing: %d Bad: %d", result.OK, result.Missing, result.Bad))
		for _, fileName := range result.MissingFiles {
			lines = append(lines, "MISSING: "+fileName)
		}
		for _, fileName := range result.OfflineFiles {
			lines = append(lines, "OFFLINE: "+fileName)
		}
		for _, fileName := range result.FailedFiles {
			lines = append(lines, "CHECKSUM FAILED: "+fileName)
		}
		for _, fileName := range result.BadFiles {
			lines = append(lines, "BAD: "+fileName)
		}
	}
	if result.ZipChecked > 0 || result.ZipBad > 0 || result.DIZRecovered {
		lines = append(lines, fmt.Sprintf("ZIP OK: %d Bad: %d", maxInt(0, result.ZipChecked-result.ZipBad), result.ZipBad))
		for _, fileName := range result.BadZips {
			lines = append(lines, "ZIP BAD: "+fileName)
		}
		if result.DIZRecovered {
			lines = append(lines, "ZIP DIZ: recovered file_id.diz")
		}
	}
	return lines
}

func resolveSitePath(currentDir, target string) string {
	if strings.HasPrefix(target, "/") {
		return path.Clean(target)
	}
	return path.Clean(path.Join(currentDir, target))
}

func expandRescanTargets(bridge MasterBridge, target string, recursive bool) []string {
	if strings.HasSuffix(target, "/*") {
		parent := strings.TrimSuffix(target, "/*")
		entries := bridge.ListDir(parent)
		out := make([]string, 0, len(entries))
		for _, entry := range entries {
			if entry.IsDir {
				releasePath := path.Join(parent, entry.Name)
				out = append(out, releasePath)
				if recursive {
					out = append(out, expandRescanTargets(bridge, releasePath+"/*", true)...)
				}
			}
		}
		sort.Strings(out)
		return dedupeSortedPaths(out)
	}
	if recursive {
		out := []string{target}
		entries := bridge.ListDir(target)
		for _, entry := range entries {
			if entry.IsDir {
				out = append(out, expandRescanTargets(bridge, path.Join(target, entry.Name), true)...)
			}
		}
		sort.Strings(out)
		return dedupeSortedPaths(out)
	}
	return []string{target}
}

func dedupeSortedPaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}
	out := paths[:0]
	var prev string
	for _, p := range paths {
		if p == "" || p == prev {
			continue
		}
		out = append(out, p)
		prev = p
	}
	return out
}

func findSFV(bridge MasterBridge, dirPath string) (string, bool) {
	entries := bridge.ListDir(dirPath)
	sfvs := make([]string, 0, 1)
	for _, entry := range entries {
		if entry.IsDir {
			continue
		}
		if strings.HasSuffix(strings.ToLower(entry.Name), ".sfv") {
			sfvs = append(sfvs, entry.Name)
		}
	}
	sort.Strings(sfvs)
	if len(sfvs) == 0 {
		return "", false
	}
	return sfvs[0], true
}

func rescanZipPostProcess(cfg *Config, bridge MasterBridge, dirPath string, opts rescanOptions) zipRescanResult {
	result := zipRescanResult{}
	if cfg == nil || bridge == nil || !zipscript.UsesZip(cfg.Zipscript, dirPath) {
		return result
	}

	entries := bridge.ListDir(dirPath)
	zipArchives := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink || entry.Size <= 0 || entry.XferTime <= 0 {
			continue
		}
		if strings.HasSuffix(strings.ToLower(strings.TrimSpace(entry.Name)), ".zip") {
			archivePath := path.Join(dirPath, entry.Name)
			if activeUploadForPathWithBridge(bridge, archivePath) {
				continue
			}
			zipArchives = append(zipArchives, entry.Name)
		}
	}
	sort.Strings(zipArchives)

	for _, archiveName := range zipArchives {
		archivePath := path.Join(dirPath, archiveName)
		if zipscript.CheckZipIntegrityForDir(cfg.Zipscript, dirPath) {
			ok, err := bridge.CheckZipIntegrity(archivePath)
			if err != nil {
				result.Errors = append(result.Errors, fmt.Sprintf("%s integrity skipped: %v", archiveName, err))
				continue
			}
			result.Checked++
			if ok {
				continue
			}
			result.Bad++
			result.BadFiles = append(result.BadFiles, archiveName)
			if opts.DeleteBad {
				if err := bridge.DeleteFile(archivePath); err != nil {
					result.Errors = append(result.Errors, fmt.Sprintf("%s delete failed: %v", archiveName, err))
				}
			}
		}
	}

	if bridge.GetFileSize(path.Join(dirPath, "file_id.diz")) < 0 {
		if _, err := zipscript.RecoverZipDIZFromDirectory(zipBridge(bridge), dirPath); err == nil {
			result.DIZRecovered = true
		} else if len(zipArchives) > 0 && cfg.Debug {
			result.Errors = append(result.Errors, fmt.Sprintf("file_id.diz recover skipped: %v", err))
		}
	}
	dirEntries := bridge.ListDir(dirPath)
	if expected := zipscript.ZipExpectedPartsFromDIZ(zipBridge(bridge), dirPath, true); expected > 0 {
		_, _, present := zipDirRaceStats(bridge, dirPath, dirEntries, expected)
		zipscript.CacheZipReleaseProgress(zipBridge(bridge), dirPath, present, expected)
	}
	return result
}

func shouldRefreshRescanMediaInfo(cfg *Config, dirPath string) bool {
	if cfg == nil {
		return false
	}
	cfg.Zipscript.ApplyDefaults()
	if !cfg.Zipscript.Race.MusicCompleteGenre {
		return false
	}
	section, _ := zipscript.SectionInfoFromPath(dirPath)
	switch strings.ToUpper(strings.TrimSpace(section)) {
	case "MP3", "FLAC":
		return true
	default:
		return false
	}
}

func findAudioRescanCandidate(bridge MasterBridge, dirPath string) (string, bool) {
	entries := bridge.ListDir(dirPath)
	audioFiles := make([]string, 0, len(entries))
	audioExts := map[string]bool{"mp3": true, "flac": true, "m4a": true, "wav": true}
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink || entry.Size <= 0 || entry.XferTime <= 0 {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(strings.TrimSpace(entry.Name)), "."))
		if !audioExts[ext] {
			continue
		}
		candidatePath := path.Join(dirPath, entry.Name)
		if activeUploadForPathWithBridge(bridge, candidatePath) {
			continue
		}
		audioFiles = append(audioFiles, entry.Name)
	}
	sort.Strings(audioFiles)
	if len(audioFiles) == 0 {
		return "", false
	}
	return path.Join(dirPath, audioFiles[0]), true
}

func findFirstUsableAudioInfo(bridge MasterBridge, cfg *Config, dirPath string) (string, map[string]string, bool) {
	if bridge == nil || cfg == nil {
		return "", nil, false
	}
	entries := bridge.ListDir(dirPath)
	audioFiles := make([]string, 0, len(entries))
	audioExts := make(map[string]bool)
	for _, ext := range cfg.Zipscript.Audio.Extensions {
		ext = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(ext, ".")))
		if ext != "" {
			audioExts[ext] = true
		}
	}
	if len(audioExts) == 0 {
		audioExts = map[string]bool{"mp3": true, "flac": true, "m4a": true, "wav": true}
	}
	for _, entry := range entries {
		if entry.IsDir || entry.IsSymlink || entry.Size <= 0 || entry.XferTime <= 0 {
			continue
		}
		ext := strings.ToLower(strings.TrimPrefix(path.Ext(strings.TrimSpace(entry.Name)), "."))
		if !audioExts[ext] {
			continue
		}
		candidatePath := path.Join(dirPath, entry.Name)
		if activeUploadForPathWithBridge(bridge, candidatePath) {
			continue
		}
		audioFiles = append(audioFiles, entry.Name)
	}
	sort.Strings(audioFiles)
	if len(audioFiles) == 0 {
		return "", nil, false
	}
	for _, name := range audioFiles {
		candidatePath := path.Join(dirPath, name)
		fields, err := bridge.ProbeMediaInfo(candidatePath, "", 0)
		if err != nil || !zipscript.AudioInfoLooksUsable(fields) {
			continue
		}
		return candidatePath, fields, true
	}
	return "", nil, false
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func refreshAudioSortLinks(bridge MasterBridge, cfg zipscript.Config, releasePath string, previousFields, currentFields map[string]string) error {
	currentLinks := zipscript.AudioSortLinks(cfg, releasePath, currentFields)
	currentSet := make(map[string]struct{}, len(currentLinks))
	for _, link := range currentLinks {
		currentSet[link.LinkPath] = struct{}{}
	}
	for _, oldLink := range zipscript.AudioSortLinks(cfg, releasePath, previousFields) {
		if _, keep := currentSet[oldLink.LinkPath]; keep {
			continue
		}
		if bridge.GetFileSize(oldLink.LinkPath) >= 0 {
			if err := bridge.DeleteFile(oldLink.LinkPath); err != nil {
				return err
			}
		}
	}
	return ensureAudioSortLinks(bridge, currentLinks)
}

func cleanupAudioSortLinksForRelease(bridge MasterBridge, cfg zipscript.Config, releasePath string) error {
	if bridge == nil {
		return nil
	}
	fields := bridge.GetDirMediaInfo(releasePath)
	if len(fields) == 0 {
		return nil
	}
	for _, link := range zipscript.AudioSortLinks(cfg, releasePath, fields) {
		if bridge.GetFileSize(link.LinkPath) >= 0 {
			if err := bridge.DeleteFile(link.LinkPath); err != nil {
				return err
			}
		}
	}
	return nil
}

func releaseHasMediaInfoFiles(bridge MasterBridge, releasePath string) bool {
	if bridge == nil {
		return false
	}
	for _, entry := range bridge.ListDir(releasePath) {
		if entry.IsDir || entry.IsSymlink {
			continue
		}
		if zipscript.IsMediaInfoFile(entry.Name) {
			return true
		}
	}
	return false
}

func maybeClearReleaseMediaInfoAndLinks(bridge MasterBridge, cfg zipscript.Config, releasePath string, previousFields map[string]string) error {
	return clearReleaseMediaInfoAndLinks(bridge, cfg, releasePath, previousFields, false)
}

func clearReleaseMediaInfoAndLinks(bridge MasterBridge, cfg zipscript.Config, releasePath string, previousFields map[string]string, force bool) error {
	if bridge == nil {
		return nil
	}
	if !force && releaseHasMediaInfoFiles(bridge, releasePath) {
		return nil
	}
	if len(previousFields) == 0 {
		previousFields = bridge.GetDirMediaInfo(releasePath)
	}
	for _, link := range zipscript.AudioSortLinks(cfg, releasePath, previousFields) {
		if bridge.GetFileSize(link.LinkPath) >= 0 {
			if err := bridge.DeleteFile(link.LinkPath); err != nil {
				return err
			}
		}
	}
	bridge.CacheMediaInfo(releasePath, nil)
	return nil
}

func maybeRefreshReleaseMediaInfoAndLinks(cfg *Config, bridge MasterBridge, releasePath string, previousFields map[string]string) error {
	if bridge == nil || cfg == nil {
		return nil
	}
	if !releaseHasMediaInfoFiles(bridge, releasePath) {
		return maybeClearReleaseMediaInfoAndLinks(bridge, cfg.Zipscript, releasePath, previousFields)
	}
	if !shouldRefreshRescanMediaInfo(cfg, releasePath) {
		return nil
	}
	_, fields, ok := findFirstUsableAudioInfo(bridge, cfg, releasePath)
	if !ok {
		return clearReleaseMediaInfoAndLinks(bridge, cfg.Zipscript, releasePath, previousFields, true)
	}
	if len(previousFields) == 0 {
		previousFields = cloneStringMap(bridge.GetDirMediaInfo(releasePath))
	}
	bridge.CacheMediaInfo(releasePath, fields)
	return refreshAudioSortLinks(bridge, cfg.Zipscript, releasePath, previousFields, fields)
}
