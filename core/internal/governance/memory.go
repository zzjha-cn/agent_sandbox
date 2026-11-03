package governance

import (
	"errors"
	"strconv"

	"sandx/internal/projectmemory"
)

// MemoryStore binds organization writes to the actual host UID. The fixed,
// root-owned organization policy is the authority, not --reviewer or SBX_HOME.
// As elsewhere in governance this does not defend against a host administrator
// or direct edits by the owner of the local store.
func (s Snapshot) MemoryStore(home string, uid int, mutate bool) (projectmemory.Store, error) {
	store := projectmemory.Store{Home: home}
	if !s.Managed || !mutate {
		return store, nil
	}
	if e := s.Active(); e != nil {
		return store, e
	}
	grant, ok := s.Policy.MemoryReviewers[strconv.Itoa(uid)]
	if !ok || len(grant.Projects) == 0 {
		return store, errors.New("host UID has no organization memory review grant")
	}
	store.AllowedProjects = append([]string{}, grant.Projects...)
	store.Actor = &projectmemory.ReviewIdentity{Subject: grant.Subject, Organization: s.Policy.Organization, PolicyDigest: s.Digest}
	return store, nil
}
