package projectmemory

import "context"

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
