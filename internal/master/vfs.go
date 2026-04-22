package master

import (
	"encoding/gob"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// VFSFile represents a file or directory in the master's virtual file system.
type VFSFile struct {
	Path         string
	Size         int64
	IsDir        bool
	IsSymlink    bool
	LinkTarget   string
	Mode         uint32
	LastModified int64
	SlaveName    string
	Owner        string
	Group        string
	Seen         bool // Used to detect and purge deleted ghost files
	XferTime     int64 // transfer time in milliseconds (for speed calc)
	Checksum     uint32 // CRC32 from transfer
}

// VFSDirMeta holds per-directory metadata cached on the VFS (like drftpd's pluginMetaData).
// Stored separately from file entries.
type VFSDirMeta struct {
	SFVEntries map[string]uint32 // filename -> CRC32 from parsed SFV
	SFVName    string            // name of the .sfv file
}

type VFSSearchResult struct {
	Path    string
	Files   int
	Bytes   int64
	ModTime int64
}

// VirtualFileSystem maintains the master's view of files across all slaves.
//  / VirtualFileSystemDirectory.
type VirtualFileSystem struct {
	files         map[string]*VFSFile
	dirMeta       map[string]*VFSDirMeta // dir path -> metadata (SFV cache etc)
	protectedDirs map[string]bool
	mu            sync.RWMutex
}

func NewVirtualFileSystem() *VirtualFileSystem {
	vfs := &VirtualFileSystem{
		files:         make(map[string]*VFSFile),
		dirMeta:       make(map[string]*VFSDirMeta),
		protectedDirs: make(map[string]bool),
	}
	vfs.files["/"] = &VFSFile{Path: "/", IsDir: true, Seen: true}
	return vfs
}

func (vfs *VirtualFileSystem) AddFile(path string, file VFSFile) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	// Normalize path
	path = cleanVFSPath(path)
	file.Path = path
	if file.LinkTarget != "" {
		file.LinkTarget = cleanVFSPath(file.LinkTarget)
	}
	if vfs.protectedDirs == nil {
		vfs.protectedDirs = make(map[string]bool)
	}
	if file.IsDir && filepath.Dir(path) == "/" && path != "/" {
		vfs.protectedDirs[path] = true
	}
	if vfs.protectedDirs[path] {
		file.IsDir = true
		file.IsSymlink = false
		file.LinkTarget = ""
		file.Seen = true
		file.SlaveName = ""
	}
	if file.IsSymlink && file.LinkTarget != "" {
		if existing := vfs.files[path]; existing != nil && existing.IsDir {
			file.IsDir = true
		}
		if target := vfs.files[file.LinkTarget]; target != nil && target.IsDir {
			file.IsDir = true
		}
	}

	vfs.files[path] = &file

	// Ensure parent dirs exist
	dir := filepath.Dir(path)
	for dir != "/" && dir != "." {
		if existing, exists := vfs.files[dir]; !exists {
			vfs.files[dir] = &VFSFile{
				Path:      dir,
				IsDir:     true,
				SlaveName: file.SlaveName,
				Seen:      true, // Keep parent directories alive
			}
		} else {
			// Ensure existing parent directories are also marked as seen so they aren't purged
			existing.Seen = true
		}
		dir = filepath.Dir(dir)
	}
}

func (vfs *VirtualFileSystem) AddSymlink(linkPath, targetPath string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	linkPath = cleanVFSPath(linkPath)
	targetPath = cleanVFSPath(targetPath)
	if existing := vfs.files[linkPath]; existing != nil && existing.IsSymlink && existing.LinkTarget == targetPath {
		existing.Seen = true
		return
	}
	vfs.files[linkPath] = &VFSFile{
		Path:         linkPath,
		IsDir:        true,
		IsSymlink:    true,
		LinkTarget:   targetPath,
		Mode:         0777,
		LastModified: time.Now().Unix(),
		Seen:         true,
	}
}

func (vfs *VirtualFileSystem) Chmod(path string, mode uint32) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	path = cleanVFSPath(path)
	if f := vfs.files[path]; f != nil {
		f.Mode = mode
		f.LastModified = time.Now().Unix()
	}
}

// MarkAllUnseen flags all files for a specific slave as unseen before a remerge.
func (vfs *VirtualFileSystem) MarkAllUnseen(slaveName string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()
	for path, file := range vfs.files {
		if vfs.protectedDirs[path] {
			file.Seen = true
			file.SlaveName = ""
			continue
		}
		if file.SlaveName == slaveName {
			file.Seen = false
		}
	}
}

// PurgeUnseen removes any files for a specific slave that were not seen during the remerge.
func (vfs *VirtualFileSystem) PurgeUnseen(slaveName string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()
	for path, file := range vfs.files {
		if vfs.protectedDirs[path] {
			file.Seen = true
			file.SlaveName = ""
			continue
		}
		if file.SlaveName == slaveName && !file.Seen {
			delete(vfs.files, path)
		}
	}
}

func (vfs *VirtualFileSystem) SetProtectedDirs(paths []string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	vfs.protectedDirs = make(map[string]bool, len(paths)+1)
	vfs.protectedDirs["/"] = true
	for p, f := range vfs.files {
		if f != nil && f.IsDir && filepath.Dir(cleanVFSPath(p)) == "/" && cleanVFSPath(p) != "/" {
			vfs.protectedDirs[cleanVFSPath(p)] = true
		}
	}
	for _, p := range paths {
		p = cleanVFSPath(p)
		if p == "" || p == "." {
			continue
		}
		vfs.protectedDirs[p] = true
		f := vfs.files[p]
		if f != nil {
			f.Path = p
			f.IsDir = true
			f.Seen = true
			f.SlaveName = ""
		}
	}
}

func (vfs *VirtualFileSystem) GetFile(path string) *VFSFile {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	return vfs.files[filepath.Clean(path)]
}

func (vfs *VirtualFileSystem) DeleteFile(path string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	path = filepath.Clean(path)
	delete(vfs.files, path)

	// Also delete children if directory
	prefix := path + "/"
	for k := range vfs.files {
		if strings.HasPrefix(k, prefix) {
			delete(vfs.files, k)
		}
	}
}

// ListDirectory returns direct children of a directory.
func (vfs *VirtualFileSystem) ListDirectory(dirPath string) []*VFSFile {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	dirPath = filepath.Clean(dirPath)
	if !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	if dirPath == "//" {
		dirPath = "/"
	}

	var results []*VFSFile
	seen := make(map[string]bool)

	for path, file := range vfs.files {
		if path == dirPath || path == strings.TrimSuffix(dirPath, "/") {
			continue // skip self
		}

		if !strings.HasPrefix(path, dirPath) {
			continue
		}

		// Only direct children (no deeper nesting)
		remainder := path[len(dirPath):]
		if strings.Contains(remainder, "/") {
			continue
		}

		if !seen[path] {
			seen[path] = true
			results = append(results, file)
		}
	}

	return results
}

// FileExists checks if a path exists in the VFS
func (vfs *VirtualFileSystem) FileExists(path string) bool {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	_, exists := vfs.files[filepath.Clean(path)]
	return exists
}

// GetSlavesForPath returns the names of all slaves that have this file
func (vfs *VirtualFileSystem) GetSlavesForPath(path string) []string {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	file := vfs.files[filepath.Clean(path)]
	if file == nil {
		return nil
	}
	return []string{file.SlaveName}
}

// RenameFile renames a file/dir in the VFS
func (vfs *VirtualFileSystem) RenameFile(from, to string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	from = filepath.Clean(from)
	to = filepath.Clean(to)

	file := vfs.files[from]
	if file == nil {
		return
	}

	// Move the file itself
	delete(vfs.files, from)
	file.Path = to
	vfs.files[to] = file

	// Move children if directory
	prefix := from + "/"
	var toMove []struct{ old, new string }
	for k := range vfs.files {
		if strings.HasPrefix(k, prefix) {
			newPath := to + "/" + k[len(prefix):]
			toMove = append(toMove, struct{ old, new string }{k, newPath})
		}
	}
	for _, mv := range toMove {
		f := vfs.files[mv.old]
		delete(vfs.files, mv.old)
		f.Path = mv.new
		vfs.files[mv.new] = f
	}

	metaPrefix := from + "/"
	var metaMove []struct{ old, new string }
	for k := range vfs.dirMeta {
		if k == from || strings.HasPrefix(k, metaPrefix) {
			metaMove = append(metaMove, struct{ old, new string }{k, to + k[len(from):]})
		}
	}
	for _, mv := range metaMove {
		meta := vfs.dirMeta[mv.old]
		delete(vfs.dirMeta, mv.old)
		vfs.dirMeta[mv.new] = meta
	}
}

// ClearSlave removes all files belonging to a slave (called when slave goes offline)
func (vfs *VirtualFileSystem) ClearSlave(slaveName string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	for path, file := range vfs.files {
		if file.SlaveName == slaveName {
			delete(vfs.files, path)
		}
	}
}

// GetAllFiles returns all files in the VFS (for debugging)
func (vfs *VirtualFileSystem) GetAllFiles() map[string]*VFSFile {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	result := make(map[string]*VFSFile, len(vfs.files))
	for k, v := range vfs.files {
		result[k] = v
	}
	return result
}

func (vfs *VirtualFileSystem) SearchDirs(query string, limit int) []VFSSearchResult {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" {
		return nil
	}
	if limit <= 0 {
		limit = 100
	}

	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	dirs := make([]*VFSFile, 0)
	for _, f := range vfs.files {
		if f == nil || !f.IsDir || f.Path == "/" {
			continue
		}
		cleanPath := filepath.ToSlash(filepath.Clean(f.Path))
		base := filepath.Base(cleanPath)
		if strings.Contains(strings.ToLower(cleanPath), query) || strings.Contains(strings.ToLower(base), query) {
			dirs = append(dirs, f)
		}
	}

	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(filepath.ToSlash(dirs[i].Path)) < strings.ToLower(filepath.ToSlash(dirs[j].Path))
	})

	now := time.Now().Unix()
	results := make([]VFSSearchResult, 0, minInt(limit, len(dirs)))
	for _, dir := range dirs {
		if len(results) >= limit {
			break
		}
		dirPath := filepath.ToSlash(filepath.Clean(dir.Path))
		prefix := strings.TrimRight(dirPath, "/") + "/"
		res := VFSSearchResult{
			Path:    dirPath,
			ModTime: dir.LastModified,
		}
		for _, f := range vfs.files {
			if f == nil || f.IsDir {
				continue
			}
			filePath := filepath.ToSlash(filepath.Clean(f.Path))
			if !strings.HasPrefix(filePath, prefix) {
				continue
			}
			res.Files++
			res.Bytes += f.Size
			if f.LastModified > res.ModTime {
				res.ModTime = f.LastModified
			}
		}
		if res.ModTime <= 0 {
			res.ModTime = now
		}
		results = append(results, res)
	}
	return results
}

// Count returns the number of entries in the VFS
func (vfs *VirtualFileSystem) Count() int {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	return len(vfs.files)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SaveToDisk persists the VFS to a gob file.
// Called on shutdown and periodically by the slave manager.
func (vfs *VirtualFileSystem) SaveToDisk(filePath string) error {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	dir := filepath.Dir(filePath)
	if dir != "" && dir != "." {
		os.MkdirAll(dir, 0755)
	}

	// Write to temp file first, then rename (atomic)
	tmpPath := filePath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create vfs file: %w", err)
	}

	enc := gob.NewEncoder(f)
	if err := enc.Encode(vfs.files); err != nil {
		f.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("encode vfs: %w", err)
	}
	f.Close()

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("rename vfs file: %w", err)
	}

	log.Printf("[VFS] Saved %d entries to %s", len(vfs.files), filePath)
	return nil
}

// LoadFromDisk loads the VFS from a previously saved gob file.
// Called on master startup before slaves connect.
func (vfs *VirtualFileSystem) LoadFromDisk(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("[VFS] No saved VFS at %s, starting fresh", filePath)
			return nil
		}
		return fmt.Errorf("open vfs file: %w", err)
	}
	defer f.Close()

	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	dec := gob.NewDecoder(f)
	if err := dec.Decode(&vfs.files); err != nil {
		return fmt.Errorf("decode vfs: %w", err)
	}

	// Ensure root exists
	if _, ok := vfs.files["/"]; !ok {
		vfs.files["/"] = &VFSFile{Path: "/", IsDir: true, Seen: true}
	}
	if vfs.protectedDirs == nil {
		vfs.protectedDirs = make(map[string]bool)
	}

	log.Printf("[VFS] Loaded %d entries from %s", len(vfs.files), filePath)
	return nil
}

func cleanVFSPath(p string) string {
	p = filepath.ToSlash(filepath.Clean(strings.TrimSpace(p)))
	if p == "." || p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}
// SetSFVData caches parsed SFV entries on a directory (like drftpd's pluginMetaData).
func (vfs *VirtualFileSystem) SetSFVData(dirPath string, sfvName string, entries map[string]uint32) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()
	dirPath = filepath.Clean(dirPath)
	normalized := make(map[string]uint32, len(entries))
	for name, crc := range entries {
		name = raceFileKey(name)
		if name != "" {
			normalized[name] = crc
		}
	}
	vfs.dirMeta[dirPath] = &VFSDirMeta{
		SFVEntries: normalized,
		SFVName:    sfvName,
	}
}

// GetSFVData returns cached SFV entries for a directory, or nil if not cached.
func (vfs *VirtualFileSystem) GetSFVData(dirPath string) *VFSDirMeta {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	return vfs.dirMeta[filepath.Clean(dirPath)]
}

// RaceUserStat holds per-user race statistics computed from VFS.
type RaceUserStat struct {
	Name      string
	Group     string
	Files     int
	Bytes     int64
	Speed     float64 // bytes/sec average across this user's files
	PeakSpeed float64 // bytes/sec of this user's fastest single file
	SlowSpeed float64 // bytes/sec of this user's slowest single file
	Percent   int
}

// RaceGroupStat holds per-group race statistics.
type RaceGroupStat struct {
	Name    string
	Files   int
	Bytes   int64
	Speed   float64
	Percent int
}

// GetRaceStats computes race statistics for a directory from VFS metadata.
// Like drftpd's SFVTools.getSFVFiles + RankUtils.userSort.
func (vfs *VirtualFileSystem) GetRaceStats(dirPath string) (users []RaceUserStat, groups []RaceGroupStat, totalBytes int64, present int, total int) {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	dirPath = filepath.Clean(dirPath)
	meta := vfs.dirMeta[dirPath]
	if meta == nil || len(meta.SFVEntries) == 0 {
		return
	}

	total = len(meta.SFVEntries)
	userMap := make(map[string]*RaceUserStat)
	groupMap := make(map[string]*RaceGroupStat)

	prefix := dirPath + "/"
	if dirPath == "/" {
		prefix = "/"
	}

	presentFiles := make(map[string]*VFSFile)
	for path, f := range vfs.files {
		if f == nil || f.IsDir || !strings.HasPrefix(path, prefix) {
			continue
		}
		rel := path[len(prefix):]
		if strings.Contains(rel, "/") || strings.Contains(rel, "\\") {
			continue
		}
		presentFiles[raceFileKey(rel)] = f
	}

	for sfvFile := range meta.SFVEntries {
		f := presentFiles[raceFileKey(sfvFile)]
		if f == nil {
			continue
		}
		present++
		totalBytes += f.Size

		owner := f.Owner
		if owner == "" {
			owner = "unknown"
		}
		group := f.Group
		if group == "" {
			group = "NoGroup"
		}

		// User stats
		us, ok := userMap[owner]
		if !ok {
			us = &RaceUserStat{Name: owner, Group: group}
			userMap[owner] = us
		}
		us.Files++
		us.Bytes += f.Size
		if f.XferTime > 0 {
			fileSpeed := float64(f.Size) / (float64(f.XferTime) / 1000.0)
			us.Speed += fileSpeed
			if fileSpeed > us.PeakSpeed {
				us.PeakSpeed = fileSpeed
			}
			if us.SlowSpeed == 0 || fileSpeed < us.SlowSpeed {
				us.SlowSpeed = fileSpeed
			}
		}

		// Group stats
		gs, ok := groupMap[group]
		if !ok {
			gs = &RaceGroupStat{Name: group}
			groupMap[group] = gs
		}
		gs.Files++
		gs.Bytes += f.Size
		if f.XferTime > 0 {
			gs.Speed += float64(f.Size) / (float64(f.XferTime) / 1000.0)
		}
	}

	// Calculate percentages and build sorted lists
	for _, us := range userMap {
		if total > 0 {
			us.Percent = (us.Files * 100) / total
		}
		if us.Files > 0 {
			us.Speed = us.Speed / float64(us.Files) // average speed
		}
		users = append(users, *us)
	}
	for _, gs := range groupMap {
		if total > 0 {
			gs.Percent = (gs.Files * 100) / total
		}
		if gs.Files > 0 {
			gs.Speed = gs.Speed / float64(gs.Files)
		}
		groups = append(groups, *gs)
	}

	return
}

func raceFileKey(name string) string {
	name = strings.TrimSpace(filepath.ToSlash(name))
	name = strings.TrimPrefix(name, "./")
	return strings.ToLower(name)
}
