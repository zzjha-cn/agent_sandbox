package projectmemory

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// StoreReader adapts the existing atomic JSON store. AllowedProjects and review
// identity stay on Store; a future command must construct it from trusted policy.
type StoreReader struct{ Store Store }

func (r StoreReader) ReadProject(ctx context.Context, project string) (Project, error) {
	if err := ctx.Err(); err != nil {
		return Project{}, err
	}
	v, err := r.Store.Load(project)
	if err != nil {
		return Project{}, err
	}
	return v, ctx.Err()
}

// Queries is the in-process composition root used by legacy APIs and sbx's
// adapters. Another cmd can use NewQueryService with the same or new adapters.
func (s Store) Queries() *QueryService {
	return NewQueryService(StoreReader{Store: s}, FileEvidenceResolver{Home: s.Home}, LexicalRetriever{})
}

// StoreRepository adapts management without splitting review and supersession
// into independently failing writes. Legacy callers retain the same digests.
type StoreRepository struct{ StoreReader }

func (r StoreRepository) UpdateProject(ctx context.Context, id string, sequence uint64, mutate func(*Project) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	unlock, err := r.Store.lockContext(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.Store.Load(id)
	if err != nil {
		return err
	}
	if sequence != 0 && sequence != p.Sequence {
		return errors.New("project sequence changed")
	}
	if err = mutate(&p); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	p.Sequence++
	path, err := r.Store.projectPath(id)
	if err != nil {
		return err
	}
	return write(path, p)
}
func (s Store) Management() *ManagementService {
	return NewManagementService(StoreRepository{StoreReader{s}}, s.Queries(), s.Actor)
}

func (s Store) lockContext(ctx context.Context) (func(), error) {
	if err := os.MkdirAll(s.root(), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.root(), "store.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}
