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
	Seen         bool   // Used to detect and purge deleted ghost files
	XferTime     int64  // transfer time in milliseconds (for speed calc)
	Checksum     uint32 // CRC32 from transfer
}

// VFSDirMeta holds per-directory metadata cached on the VFS (like drftpd's pluginMetaData).
// Stored separately from file entries.
type VFSDirMeta struct {
	SFVEntries map[string]uint32 // filename -> CRC32 from parsed SFV
	SFVName    string            // name of the .sfv file
	MediaInfo  map[string]string // cached release media fields, e.g. genre/year
}

type VFSRaceCache struct {
	Users      []RaceUserStat
	Groups     []RaceGroupStat
	TotalBytes int64
	Present    int
	Total      int
}

type VFSSearchResult struct {
	Path    string
	Files   int
	Bytes   int64
	ModTime int64
}

// VirtualFileSystem maintains the master's view of files across all slaves.
//
//	/ VirtualFileSystemDirectory.
type VirtualFileSystem struct {
	files         map[string]*VFSFile
	children      map[string]map[string]struct{}
	dirMeta       map[string]*VFSDirMeta // dir path -> metadata (SFV cache etc)
	raceState     map[string]*VFSRaceCache
	protectedDirs map[string]bool
	mu            sync.RWMutex
}

func NewVirtualFileSystem() *VirtualFileSystem {
	vfs := &VirtualFileSystem{
		files:         make(map[string]*VFSFile),
		children:      make(map[string]map[string]struct{}),
		dirMeta:       make(map[string]*VFSDirMeta),
		raceState:     make(map[string]*VFSRaceCache),
		protectedDirs: make(map[string]bool),
	}
	vfs.files["/"] = &VFSFile{Path: "/", IsDir: true, Seen: true}
	vfs.children["/"] = make(map[string]struct{})
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
	vfs.ensureParentDirsLocked(path, file.SlaveName)
	vfs.linkChildLocked(cleanVFSPath(filepath.Dir(path)), path)
	if file.IsDir {
		vfs.ensureChildrenBucketLocked(path)
	}
	vfs.touchAncestorsLocked(path, time.Now().Unix())
	vfs.refreshRaceStateForPathLocked(path)
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
	vfs.ensureParentDirsLocked(linkPath, "")
	vfs.linkChildLocked(cleanVFSPath(filepath.Dir(linkPath)), linkPath)
	vfs.ensureChildrenBucketLocked(linkPath)
	vfs.touchAncestorsLocked(linkPath, time.Now().Unix())
	vfs.refreshRaceStateForPathLocked(linkPath)
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
	changed := false
	for path, file := range vfs.files {
		if vfs.protectedDirs[path] {
			file.Seen = true
			file.SlaveName = ""
			continue
		}
		if file.SlaveName == slaveName && !file.Seen {
			delete(vfs.files, path)
			changed = true
		}
	}
	if changed {
		vfs.rebuildChildrenLocked()
		vfs.rebuildAllRaceStatesLocked()
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
	vfs.rebuildChildrenLocked()
	vfs.rebuildAllRaceStatesLocked()
}

func (vfs *VirtualFileSystem) GetFile(path string) *VFSFile {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	return vfs.files[filepath.Clean(path)]
}

func (vfs *VirtualFileSystem) DeleteFile(path string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	path = cleanVFSPath(path)
	parent := cleanVFSPath(filepath.Dir(path))
	delete(vfs.files, path)

	// Also delete children if directory
	prefix := path + "/"
	for k := range vfs.files {
		if strings.HasPrefix(k, prefix) {
			delete(vfs.files, k)
		}
	}
	vfs.rebuildChildrenLocked()
	vfs.touchAncestorsLocked(parent, time.Now().Unix())
	vfs.rebuildAllRaceStatesLocked()
}

// ListDirectory returns direct children of a directory.
func (vfs *VirtualFileSystem) ListDirectory(dirPath string) []*VFSFile {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()

	dirPath = cleanVFSPath(dirPath)
	childPaths := vfs.children[dirPath]
	if len(childPaths) == 0 {
		return nil
	}

	results := make([]*VFSFile, 0, len(childPaths))
	for childPath := range childPaths {
		if file := vfs.files[childPath]; file != nil {
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

	from = cleanVFSPath(from)
	to = cleanVFSPath(to)
	fromParent := cleanVFSPath(filepath.Dir(from))
	toParent := cleanVFSPath(filepath.Dir(to))

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
	vfs.rebuildChildrenLocked()
	now := time.Now().Unix()
	vfs.touchAncestorsLocked(fromParent, now)
	vfs.touchAncestorsLocked(toParent, now)
	vfs.rebuildAllRaceStatesLocked()
}

// ClearSlave removes all files belonging to a slave (called when slave goes offline)
func (vfs *VirtualFileSystem) ClearSlave(slaveName string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()

	changed := false
	for path, file := range vfs.files {
		if file.SlaveName == slaveName {
			delete(vfs.files, path)
			changed = true
		}
	}
	if changed {
		vfs.rebuildChildrenLocked()
		vfs.rebuildAllRaceStatesLocked()
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
	vfs.rebuildChildrenLocked()
	vfs.rebuildAllRaceStatesLocked()

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
	meta := vfs.dirMeta[dirPath]
	if meta == nil {
		meta = &VFSDirMeta{}
		vfs.dirMeta[dirPath] = meta
	}
	meta.SFVEntries = normalized
	meta.SFVName = sfvName
	vfs.refreshRaceStateLocked(dirPath)
}

// SetMediaInfo caches release-level mediainfo fields on a directory.
func (vfs *VirtualFileSystem) SetMediaInfo(dirPath string, fields map[string]string) {
	vfs.mu.Lock()
	defer vfs.mu.Unlock()
	dirPath = filepath.Clean(dirPath)
	meta := vfs.dirMeta[dirPath]
	if meta == nil {
		meta = &VFSDirMeta{}
		vfs.dirMeta[dirPath] = meta
	}
	meta.MediaInfo = make(map[string]string, len(fields))
	for k, v := range fields {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" {
			meta.MediaInfo[k] = v
		}
	}
}

// GetMediaInfo returns cached release-level mediainfo fields.
func (vfs *VirtualFileSystem) GetMediaInfo(dirPath string) map[string]string {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	meta := vfs.dirMeta[filepath.Clean(dirPath)]
	if meta == nil || len(meta.MediaInfo) == 0 {
		return nil
	}
	out := make(map[string]string, len(meta.MediaInfo))
	for k, v := range meta.MediaInfo {
		out[k] = v
	}
	return out
}

// GetSFVData returns cached SFV entries for a directory, or nil if not cached.
func (vfs *VirtualFileSystem) GetSFVData(dirPath string) *VFSDirMeta {
	vfs.mu.RLock()
	defer vfs.mu.RUnlock()
	return vfs.dirMeta[filepath.Clean(dirPath)]
}

// RaceUserStat holds per-user race statistics computed from VFS.
type RaceUserStat struct {
	Name       string
	Group      string
	Files      int
	Bytes      int64
	Speed      float64 // bytes/sec average across this user's files
	PeakSpeed  float64 // bytes/sec of this user's fastest single file
	SlowSpeed  float64 // bytes/sec of this user's slowest single file
	Percent    int
	DurationMs int64 // sum of file durations for this user
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
	cache := vfs.raceState[dirPath]
	if cache == nil {
		return
	}
	users = append(users, cache.Users...)
	groups = append(groups, cache.Groups...)
	totalBytes = cache.TotalBytes
	present = cache.Present
	total = cache.Total
	return
}

func raceFileKey(name string) string {
	name = strings.TrimSpace(filepath.ToSlash(name))
	name = strings.TrimPrefix(name, "./")
	return strings.ToLower(name)
}

func (vfs *VirtualFileSystem) ensureChildrenBucketLocked(dirPath string) {
	dirPath = cleanVFSPath(dirPath)
	if vfs.children == nil {
		vfs.children = make(map[string]map[string]struct{})
	}
	if _, ok := vfs.children[dirPath]; !ok {
		vfs.children[dirPath] = make(map[string]struct{})
	}
}

func (vfs *VirtualFileSystem) linkChildLocked(parentPath, childPath string) {
	parentPath = cleanVFSPath(parentPath)
	childPath = cleanVFSPath(childPath)
	vfs.ensureChildrenBucketLocked(parentPath)
	vfs.children[parentPath][childPath] = struct{}{}
}

func (vfs *VirtualFileSystem) ensureParentDirsLocked(path string, slaveName string) {
	path = cleanVFSPath(path)
	if _, ok := vfs.files["/"]; !ok {
		vfs.files["/"] = &VFSFile{Path: "/", IsDir: true, Seen: true}
	}
	vfs.ensureChildrenBucketLocked("/")

	dir := cleanVFSPath(filepath.Dir(path))
	for dir != "." && dir != "" {
		if existing, exists := vfs.files[dir]; !exists {
			vfs.files[dir] = &VFSFile{
				Path:      dir,
				IsDir:     true,
				SlaveName: slaveName,
				Seen:      true,
			}
		} else {
			existing.Path = dir
			existing.IsDir = true
			existing.Seen = true
		}
		vfs.ensureChildrenBucketLocked(dir)
		if dir == "/" {
			break
		}
		parent := cleanVFSPath(filepath.Dir(dir))
		vfs.linkChildLocked(parent, dir)
		dir = parent
	}
}

func (vfs *VirtualFileSystem) touchAncestorsLocked(path string, ts int64) {
	if ts <= 0 {
		ts = time.Now().Unix()
	}
	current := cleanVFSPath(path)
	for current != "." && current != "" {
		if f := vfs.files[current]; f != nil && f.IsDir {
			f.LastModified = ts
		}
		if current == "/" {
			break
		}
		current = cleanVFSPath(filepath.Dir(current))
	}
}

func (vfs *VirtualFileSystem) rebuildChildrenLocked() {
	if vfs.files == nil {
		vfs.files = make(map[string]*VFSFile)
	}
	if _, ok := vfs.files["/"]; !ok {
		vfs.files["/"] = &VFSFile{Path: "/", IsDir: true, Seen: true}
	}

	for path, file := range vfs.files {
		if file == nil {
			delete(vfs.files, path)
			continue
		}
		cleanPath := cleanVFSPath(path)
		if cleanPath != path {
			delete(vfs.files, path)
			if _, exists := vfs.files[cleanPath]; !exists {
				file.Path = cleanPath
				vfs.files[cleanPath] = file
			}
			continue
		}
		file.Path = cleanPath
	}

	paths := make([]string, 0, len(vfs.files))
	for path := range vfs.files {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		di := strings.Count(paths[i], "/")
		dj := strings.Count(paths[j], "/")
		if di != dj {
			return di < dj
		}
		return paths[i] < paths[j]
	})
	for _, path := range paths {
		file := vfs.files[path]
		if file == nil {
			continue
		}
		vfs.ensureParentDirsLocked(path, file.SlaveName)
	}

	children := make(map[string]map[string]struct{})
	for path, file := range vfs.files {
		if file != nil && file.IsDir {
			children[path] = make(map[string]struct{})
		}
	}
	if _, ok := children["/"]; !ok {
		children["/"] = make(map[string]struct{})
	}
	for path := range vfs.files {
		if path == "/" {
			continue
		}
		parent := cleanVFSPath(filepath.Dir(path))
		if _, ok := children[parent]; !ok {
			children[parent] = make(map[string]struct{})
		}
		children[parent][path] = struct{}{}
	}
	vfs.children = children
}

func (vfs *VirtualFileSystem) rebuildAllRaceStatesLocked() {
	if vfs.raceState == nil {
		vfs.raceState = make(map[string]*VFSRaceCache)
	}
	for dirPath := range vfs.raceState {
		delete(vfs.raceState, dirPath)
	}
	for dirPath, meta := range vfs.dirMeta {
		if meta == nil || len(meta.SFVEntries) == 0 {
			continue
		}
		vfs.refreshRaceStateLocked(dirPath)
	}
}

func (vfs *VirtualFileSystem) refreshRaceStateForPathLocked(path string) {
	dirPath := cleanVFSPath(filepath.Dir(path))
	vfs.refreshRaceStateLocked(dirPath)
}

func (vfs *VirtualFileSystem) refreshRaceStateLocked(dirPath string) {
	dirPath = cleanVFSPath(dirPath)
	meta := vfs.dirMeta[dirPath]
	if meta == nil || len(meta.SFVEntries) == 0 {
		delete(vfs.raceState, dirPath)
		return
	}
	if vfs.raceState == nil {
		vfs.raceState = make(map[string]*VFSRaceCache)
	}

	userMap := make(map[string]*RaceUserStat)
	groupMap := make(map[string]*RaceGroupStat)
	presentFiles := make(map[string]*VFSFile)
	for childPath := range vfs.children[dirPath] {
		f := vfs.files[childPath]
		if f == nil || f.IsDir {
			continue
		}
		presentFiles[raceFileKey(filepath.Base(childPath))] = f
	}

	cache := &VFSRaceCache{
		Total: len(meta.SFVEntries),
	}
	for sfvFile := range meta.SFVEntries {
		f := presentFiles[raceFileKey(sfvFile)]
		if f == nil {
			continue
		}
		cache.Present++
		cache.TotalBytes += f.Size

		owner := f.Owner
		if owner == "" {
			owner = "unknown"
		}
		group := f.Group
		if group == "" {
			group = "NoGroup"
		}

		us := userMap[owner]
		if us == nil {
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
			us.DurationMs += f.XferTime
		}

		gs := groupMap[group]
		if gs == nil {
			gs = &RaceGroupStat{Name: group}
			groupMap[group] = gs
		}
		gs.Files++
		gs.Bytes += f.Size
		if f.XferTime > 0 {
			gs.Speed += float64(f.Size) / (float64(f.XferTime) / 1000.0)
		}
	}

	cache.Users = make([]RaceUserStat, 0, len(userMap))
	for _, us := range userMap {
		if cache.Total > 0 {
			us.Percent = (us.Files * 100) / cache.Total
		}
		if us.Files > 0 {
			us.Speed = us.Speed / float64(us.Files)
		}
		cache.Users = append(cache.Users, *us)
	}
	sort.Slice(cache.Users, func(i, j int) bool {
		if cache.Users[i].Files != cache.Users[j].Files {
			return cache.Users[i].Files > cache.Users[j].Files
		}
		if cache.Users[i].Bytes != cache.Users[j].Bytes {
			return cache.Users[i].Bytes > cache.Users[j].Bytes
		}
		return strings.ToLower(cache.Users[i].Name) < strings.ToLower(cache.Users[j].Name)
	})

	cache.Groups = make([]RaceGroupStat, 0, len(groupMap))
	for _, gs := range groupMap {
		if cache.Total > 0 {
			gs.Percent = (gs.Files * 100) / cache.Total
		}
		if gs.Files > 0 {
			gs.Speed = gs.Speed / float64(gs.Files)
		}
		cache.Groups = append(cache.Groups, *gs)
	}
	sort.Slice(cache.Groups, func(i, j int) bool {
		if cache.Groups[i].Files != cache.Groups[j].Files {
			return cache.Groups[i].Files > cache.Groups[j].Files
		}
		if cache.Groups[i].Bytes != cache.Groups[j].Bytes {
			return cache.Groups[i].Bytes > cache.Groups[j].Bytes
		}
		return strings.ToLower(cache.Groups[i].Name) < strings.ToLower(cache.Groups[j].Name)
	})

	vfs.raceState[dirPath] = cache
}
