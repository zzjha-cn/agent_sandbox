package projectmemory

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const SelectorVersion = "sbx.lexical/v1"

// Experience separates observed results from explanations, without granting
// model-produced observations the authority of execution records.
type Experience struct {
	Conditions   string `json:"conditions,omitempty"`
	Symptoms     string `json:"symptoms,omitempty"`
	Actions      string `json:"actions,omitempty"`
	Observations string `json:"observations,omitempty"`
	Validation   string `json:"validation,omitempty"`
	Unknowns     string `json:"unknowns,omitempty"`
}

var factKey = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,255}$`)
var sensitiveFactKey = regexp.MustCompile(`(?i)(password|credential|api[_-]?key|access[_-]?token|secret)`)

// ValidateFacts only checks bounded equality facts. These values come from the
// operator/host, never from the queried memory's claims about its own validity.
func ValidateFacts(facts map[string]string) error {
	if len(facts) > 32 {
		return errors.New("at most 32 memory facts are allowed")
	}
	for k, v := range facts {
		if !factKey.MatchString(k) || sensitiveFactKey.MatchString(k) || v == "" || len(v) > 256 || !utf8.ValidString(v) || strings.ContainsAny(v, "\r\n\x00") {
			return errors.New("memory facts require bounded keys and nonempty single-line values")
		}
		if strings.HasPrefix(k, "file:") {
			if err := ValidateWatch([]string{strings.TrimPrefix(k, "file:")}); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateKnowledge(v Input) error {
	switch v.MemoryType {
	case "", "episodic", "semantic", "procedural":
	default:
		return errors.New("unsupported memory_type")
	}
	if err := ValidateFacts(v.Requires); err != nil {
		return err
	}
	if v.Experience != nil {
		for _, text := range []string{v.Experience.Conditions, v.Experience.Symptoms, v.Experience.Actions, v.Experience.Observations, v.Experience.Validation, v.Experience.Unknowns} {
			if len(text) > 2048 || !utf8.ValidString(text) {
				return errors.New("experience field exceeds 2048 bytes")
			}
		}
	}
	return nil
}

func validateQuery(q Selection) error {
	if len(q.Query) > 2048 || !utf8.ValidString(q.Query) || len(terms(q.Query)) > 64 || strings.ContainsRune(q.Query, '\x00') {
		return errors.New("memory query exceeds 2048 bytes or 64 terms")
	}
	if strings.TrimSpace(q.Query) != "" && len(terms(q.Query)) == 0 {
		return errors.New("memory query must contain searchable terms")
	}
	if q.Limit < 0 || q.Limit > MaxEntries || q.Bytes < 0 || q.Bytes > MaxBytes || q.Bytes > 0 && q.Bytes < 256 {
		return errors.New("memory budgets: limit 1-20, max_bytes 256-16384; zero uses defaults")
	}
	return ValidateFacts(q.Facts)
}

type Decision struct {
	ID         string   `json:"id"`
	Revision   int      `json:"revision"`
	Digest     string   `json:"digest"`
	Reason     string   `json:"reason"`
	Score      int      `json:"score,omitempty"`
	Matched    []string `json:"matched_terms,omitempty"`
	Conditions string   `json:"applicability"`
	Evidence   string   `json:"evidence_status"`
}

// Applicability does not mutate approval. Missing observations are unknown,
// never assumed true; a known mismatch wins over missing facts.
func Applicability(entry Entry, facts map[string]string) string {
	state := "satisfied"
	for key, expected := range entry.Requires {
		actual, ok := facts[key]
		if !ok {
			state = "unknown"
			continue
		}
		if actual != expected {
			return "mismatch"
		}
	}
	return state
}

func terms(text string) []string {
	parts := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsNumber(r) && !strings.ContainsRune("._/-", r)
	})
	seen := map[string]bool{}
	out := []string{}
	for _, part := range parts {
		part = strings.Trim(part, "._/-")
		if part != "" && !seen[part] {
			seen[part] = true
			out = append(out, part)
		}
	}
	return out
}

func containsTerm(text, term string) bool {
	// Non-ASCII phrases support substrings without pretending to have semantic
	// embeddings or a Chinese word segmenter. ASCII identifiers match exactly.
	if strings.IndexFunc(term, func(r rune) bool { return r > unicode.MaxASCII }) >= 0 {
		return strings.Contains(strings.ToLower(text), term)
	}
	for _, token := range terms(text) {
		if token == term {
			return true
		}
	}
	return false
}

// LexicalRetriever is the existing deterministic local strategy. It does not
// read storage, resolve evidence, grant access or call an external model.
type LexicalRetriever struct{}

func (LexicalRetriever) Retrieve(ctx context.Context, query string, candidates []RetrievalCandidate) (RetrievalResult, error) {
	result := RetrievalResult{Version: SelectorVersion, Matches: []RetrievalMatch{}}
	queryTerms := terms(query)
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return RetrievalResult{}, err
		}
		score := 0
		matched := []string{}
		for _, term := range queryTerms {
			weight := 0
			if containsTerm(candidate.Title, term) {
				weight += 8
			}
			if containsTerm(strings.Join(candidate.Tags, " "), term) {
				weight += 6
			}
			if containsTerm(candidate.Text, term) {
				weight += 2
			}
			if weight > 0 {
				score += weight
				matched = append(matched, term)
			}
		}
		if score > 0 {
			result.Matches = append(result.Matches, RetrievalMatch{Ref: candidate.Ref, Score: score + candidate.ScopeBoost, Matched: matched})
		}
	}
	return result, nil
}

// CheckFacts is the legacy convenience API; new entry points supply a context.
func (s Store) CheckFacts(v Snapshot, facts map[string]string) error {
	return s.Queries().ValidateSnapshotFacts(context.Background(), v, facts)
}
