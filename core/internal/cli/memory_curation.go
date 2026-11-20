package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"

	"github.com/spf13/cobra"
	"sandx/internal/projectmemory"
)

func decodeMemoryDocument(file string, value any) error {
	raw, err := projectmemory.ReadFile(file, 128<<10)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("expected one JSON object")
	}
	return nil
}
func (a *App) addMemoryCurationCommands(root *cobra.Command) {
	draft := &cobra.Command{Use: "draft", Short: "Create and review source-bound rule/skill drafts; never installs or publishes"}
	for _, name := range []string{"create", "write", "show", "relations", "review"} {
		name := name
		var project, file, repo, output, entry, digest, decision, actor, reason, validation string
		c := &cobra.Command{Use: name, Args: cobra.NoArgs, Short: map[string]string{"create": "Freeze explicit approved sources and write a rule/skill draft", "write": "Recover writing an existing frozen draft without regeneration", "show": "Inspect draft identity, current source validity and optional output files", "relations": "Find drafts affected by a source entry; recheck source validity", "review": "Record explicit draft review bound to its digest and validation declarations"}[name], RunE: func(cmd *cobra.Command, args []string) error {
			mutate := name != "show" && name != "relations"
			store, err := a.memoryStore(mutate)
			if err != nil {
				return err
			}
			service := store.Curation()
			ctx := cmd.Context()
			var result any
			switch name {
			case "create":
				raw, readErr := projectmemory.ReadFile(file, 128<<10)
				if readErr != nil {
					return readErr
				}
				request, decodeErr := projectmemory.DecodeDraftRequest(raw)
				if decodeErr != nil {
					return decodeErr
				}
				if _, err = (projectmemory.FileDraftWriter{Repo: repo, Output: output}).Location(); err != nil {
					return err
				}
				record, e := service.CreateDraft(ctx, project, request)
				if e != nil {
					return e
				}
				result, err = service.WriteDraft(ctx, project, record.ID, projectmemory.FileDraftWriter{Repo: repo, Output: output})
				if err != nil {
					return errors.New("draft prepared as " + record.ID + "; output not completed: " + err.Error())
				}
			case "write":
				result, err = service.WriteDraft(ctx, project, args[0], projectmemory.FileDraftWriter{Repo: repo, Output: output})
			case "show":
				var writer projectmemory.DraftWriter
				if output != "" {
					writer = projectmemory.FileDraftWriter{Repo: repo, Output: output}
				}
				result, err = service.Inspect(ctx, project, args[0], writer)
			case "relations":
				result, err = service.SourceRelations(ctx, project, entry)
			case "review":
				review := projectmemory.DraftReview{Decision: decision, Actor: actor, Reason: reason}
				if validation != "" {
					if err = decodeMemoryDocument(validation, &review.Validation); err != nil {
						return err
					}
				}
				var writer projectmemory.DraftWriter
				if output != "" {
					writer = projectmemory.FileDraftWriter{Repo: repo, Output: output}
				}
				result, err = service.ReviewDraft(ctx, project, args[0], digest, review, writer)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(a.Out).Encode(result)
		}}
		c.Flags().StringVar(&project, "project", "", "registered authorized project")
		_ = c.MarkFlagRequired("project")
		if name != "create" && name != "relations" {
			c.Use = name + " <draft>"
			c.Args = cobra.ExactArgs(1)
		}
		if name == "create" {
			c.Flags().StringVar(&file, "file", "", "draft request JSON, explicit exact source refs and plan")
			_ = c.MarkFlagRequired("file")
		}
		if name == "relations" {
			c.Flags().StringVar(&entry, "entry", "", "optional source entry ID (default: all drafts)")
		} else {
			c.Flags().StringVar(&repo, "repo", ".", "repository root for the explicit draft directory")
			c.Flags().StringVar(&output, "output", "", "relative draft directory; cannot target active skills/instructions")
			if name == "create" || name == "write" {
				_ = c.MarkFlagRequired("output")
			}
		}
		if name == "review" {
			c.Flags().StringVar(&digest, "digest", "", "current draft record digest")
			_ = c.MarkFlagRequired("digest")
			c.Flags().StringVar(&decision, "decision", "", "approved|rejected (never publishes)")
			_ = c.MarkFlagRequired("decision")
			c.Flags().StringVar(&actor, "reviewer", "operator", "personal declaration; managed identity comes from host UID")
			c.Flags().StringVar(&reason, "reason", "", "explicit review explanation")
			_ = c.MarkFlagRequired("reason")
			c.Flags().StringVar(&validation, "validation", "", "JSON array of scenario/outcome/evidence declarations; required for approval")
		}
		draft.AddCommand(c)
	}
	root.AddCommand(draft)

	findings := &cobra.Command{Use: "findings", Short: "Inspect or acknowledge review issues without changing approval"}
	for _, name := range []string{"list", "review"} {
		name := name
		var project, id, decision, actor, reason string
		var sequence uint64
		var facts map[string]string
		c := &cobra.Command{Use: name, Args: cobra.NoArgs, Short: map[string]string{"list": "List current evidence, expiry, condition and counterexample review issues", "review": "Record explicit handling of a current finding without changing approval"}[name], RunE: func(cmd *cobra.Command, args []string) error {
			store, err := a.memoryStore(name == "review")
			if err != nil {
				return err
			}
			var result any
			if name == "list" {
				result, err = store.Management().ListReviewFindings(cmd.Context(), project, facts)
			} else {
				result, err = store.Management().ReviewFinding(cmd.Context(), project, projectmemory.FindingReview{FindingID: id, Decision: decision, Actor: actor, Reason: reason}, sequence, facts)
			}
			if err != nil {
				return err
			}
			return json.NewEncoder(a.Out).Encode(result)
		}}
		c.Flags().StringVar(&project, "project", "", "registered authorized project")
		_ = c.MarkFlagRequired("project")
		c.Flags().StringToStringVar(&facts, "facts", nil, "trusted equality facts for the current review view")
		if name == "review" {
			c.Flags().StringVar(&id, "finding", "", "current finding ID")
			_ = c.MarkFlagRequired("finding")
			c.Flags().StringVar(&decision, "decision", "", "acknowledged|needs_update|dismissed")
			_ = c.MarkFlagRequired("decision")
			c.Flags().StringVar(&actor, "reviewer", "operator", "personal declaration; managed identity comes from host UID")
			c.Flags().StringVar(&reason, "reason", "", "explicit finding review explanation")
			_ = c.MarkFlagRequired("reason")
			c.Flags().Uint64Var(&sequence, "sequence", 0, "expected project sequence; required in organization mode")
		}
		findings.AddCommand(c)
	}
	root.AddCommand(findings)
}
