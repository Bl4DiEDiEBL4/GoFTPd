package core

import (
	"path"
	"strings"
)

func (s *Session) canDeletePath(vpath string) bool {
	if s == nil || s.ACLEngine == nil {
		return false
	}
	aclPath := path.Join(s.Config.ACLBasePath, vpath)
	if s.ACLEngine.CanPerform(s.User, "DELETE", aclPath) {
		return true
	}
	if !s.ACLEngine.CanPerform(s.User, "DELETEOWN", aclPath) {
		return false
	}
	return s.pathOwnedByUser(vpath)
}

func (s *Session) canRemoveDirPath(vpath string) bool {
	if s == nil || s.ACLEngine == nil {
		return false
	}
	aclPath := path.Join(s.Config.ACLBasePath, vpath)
	if s.ACLEngine.CanPerform(s.User, "RMD", aclPath) && s.ACLEngine.CanPerform(s.User, "DELETE", aclPath) {
		return true
	}
	if !s.ACLEngine.CanPerform(s.User, "RMD", aclPath) {
		return false
	}
	if !s.ACLEngine.CanPerform(s.User, "DELETEOWN", aclPath) {
		return false
	}
	return s.pathOwnedByUser(vpath)
}

func (s *Session) canRenamePath(fromPath, toPath string) bool {
	if s == nil || s.ACLEngine == nil {
		return false
	}
	toACLPath := path.Join(s.Config.ACLBasePath, toPath)
	if s.ACLEngine.CanPerform(s.User, "RENAME", toACLPath) {
		return true
	}
	fromACLPath := path.Join(s.Config.ACLBasePath, fromPath)
	if !s.ACLEngine.CanPerform(s.User, "RENAMEOWN", fromACLPath) {
		return false
	}
	// RENAMEOWN only grants renaming files the user owns; the destination
	// still has to be somewhere the user is generally allowed to place a
	// renamed file, otherwise an owned-file rename can land anywhere ACLed
	// paths would otherwise forbid.
	if !s.ACLEngine.CanPerform(s.User, "RENAMEOWN", toACLPath) {
		return false
	}
	return s.pathOwnedByUser(fromPath)
}

func (s *Session) pathOwnedByUser(vpath string) bool {
	if s == nil || s.User == nil || strings.TrimSpace(s.User.Name) == "" {
		return false
	}
	if s.Config == nil || s.Config.Mode != "master" || s.MasterManager == nil {
		return false
	}
	bridge, ok := s.MasterManager.(MasterBridge)
	if !ok || bridge == nil {
		return false
	}
	vpath = path.Clean(vpath)
	parent := path.Dir(vpath)
	name := path.Base(vpath)
	for _, entry := range bridge.ListDir(parent) {
		if strings.EqualFold(entry.Name, name) {
			return strings.EqualFold(entry.Owner, s.User.Name)
		}
	}
	return false
}
