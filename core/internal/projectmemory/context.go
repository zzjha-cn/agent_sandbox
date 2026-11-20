package projectmemory

import (
	"context"
	"errors"
	"slices"
)

// ObservedFacts is a detached trusted observation, never supplied by tool input.
// Binding identifies values, not proof that operator assertions are true.
type ObservedFacts struct {
	Values  map[string]string
	Binding string
}
type ObservationProvider interface {
	Observe(context.Context) (ObservedFacts, error)
}
type FileObservationProvider struct {
	Root  string
	Watch []string
	Facts map[string]string
}

func (p FileObservationProvider) Observe(ctx context.Context) (ObservedFacts, error) {
	if err := ctx.Err(); err != nil {
		return ObservedFacts{}, err
	}
	values, err := ObserveFiles(p.Root, p.Watch, p.Facts)
	if err != nil {
		return ObservedFacts{}, err
	}
	return ObservedFacts{Values: values, Binding: Digest(values)}, ctx.Err()
}

type QueryBudget struct {
	Entries int
	Bytes   int
}
type Query struct {
	Text        string
	Paths, Tags []string
	Budget      QueryBudget
}

// QueryContext is constructed only by a trusted host. Requested paths cannot
// enlarge its affected paths; requested tags can only narrow its tag set.
type QueryContext struct {
	Project      string
	Scope        Selection
	Observations ObservationProvider
}

func (s *QueryService) SearchWithin(ctx context.Context, scope QueryContext, query Query) (Snapshot, error) {
	if _, err := s.read(ctx, scope.Project); err != nil {
		return Snapshot{}, err
	}
	q := scope.Scope
	q.Query = query.Text
	if len(query.Paths) > 0 {
		for _, p := range query.Paths {
			if !slices.Contains(q.Paths, p) {
				return Snapshot{}, errors.New("query exceeds host path scope")
			}
		}
		q.Paths = append([]string(nil), query.Paths...)
	}
	if len(query.Tags) > 0 {
		for _, tag := range query.Tags {
			if len(q.Tags) > 0 && !slices.Contains(q.Tags, tag) {
				return Snapshot{}, errors.New("query exceeds host tag scope")
			}
		}
		q.Tags = append([]string(nil), query.Tags...)
	}
	if query.Budget.Entries != 0 {
		if q.Limit != 0 && query.Budget.Entries > q.Limit {
			return Snapshot{}, errors.New("query exceeds host entry budget")
		}
		q.Limit = query.Budget.Entries
	}
	if query.Budget.Bytes != 0 {
		if q.Bytes != 0 && query.Budget.Bytes > q.Bytes {
			return Snapshot{}, errors.New("query exceeds host byte budget")
		}
		q.Bytes = query.Budget.Bytes
	}
	if scope.Observations != nil {
		facts, err := scope.Observations.Observe(ctx)
		if err != nil {
			return Snapshot{}, err
		}
		if facts.Binding != Digest(facts.Values) {
			return Snapshot{}, errors.New("invalid observation binding")
		}
		q.Facts = facts.Values
	}
	return s.Search(ctx, scope.Project, q)
}
