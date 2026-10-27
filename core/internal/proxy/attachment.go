package proxy

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"sandx/internal/fsutil"
)

// BeginAttachment captures only this Task's proxy state before AttachTask.
// Rollback preserves preexisting instances and other Tasks' shared credentials.
// The caller holds the Task lock and invokes rollback on an independent context.
func BeginAttachment(e Egress, t TaskSpec) (Egress, func(context.Context) error, error) {
	switch p := e.(type) {
	case Dedicated:
		return beginDedicated(p, t)
	case Shared:
		return beginShared(p, t)
	default:
		// Alternate adapters retain the original attach/detach contract.
		return e, func(context.Context) error {
			a := e.DetachTask(t.TaskID, t.Network)
			_, b := e.StopIfIdle()
			return errors.Join(a, b)
		}, nil
	}
}

func beginDedicated(p Dedicated, t TaskSpec) (Egress, func(context.Context) error, error) {
	state, exists, err := p.Docker.Inspect(p.Name)
	if err != nil {
		return nil, nil, err
	}
	restore, err := fsutil.SnapshotPath(p.Dir)
	if err != nil {
		return nil, nil, err
	}
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, nil, err
	}
	token := hex.EncodeToString(tokenBytes)
	labels := map[string]string{}
	for k, v := range p.Labels {
		labels[k] = v
	}
	labels["sbx.provision"] = token
	p.Labels = labels
	return p, func(ctx context.Context) error {
		p.Docker = p.Docker.WithContext(ctx)
		var failures []error
		if !exists {
			items, err := p.Docker.ListByLabel("container", "sbx.provision="+token)
			if err != nil {
				failures = append(failures, err)
			} else {
				for _, item := range items {
					if item.Str("Names") == p.Name {
						target := item.Str("ID")
						if target == "" {
							target = p.Name
						}
						if err := p.Docker.Rm(target); err != nil {
							failures = append(failures, err)
						}
					}
				}
			}
		}
		if err := restore(); err != nil {
			failures = append(failures, err)
		}
		if exists {
			if state.Running {
				if err := p.reconfigure(); err != nil {
					failures = append(failures, err)
				}
			} else {
				current, found, err := p.Docker.Inspect(p.Name)
				if err != nil {
					failures = append(failures, err)
				} else if found && current.Running {
					if err := p.Docker.Stop(p.Name); err != nil {
						failures = append(failures, err)
					}
				}
			}
		}
		return errors.Join(failures...)
	}, nil
}

func beginShared(p Shared, t TaskSpec) (Egress, func(context.Context) error, error) {
	unlock, err := p.lock()
	if err != nil {
		return nil, nil, err
	}
	defer unlock()
	var restores []func() error
	for _, path := range []string{p.path("allow", t.TaskID+".txt"), p.path("block", t.TaskID+".txt"), p.path("tasks", t.TaskID+".conf"), t.CredFile} {
		restore, err := fsutil.SnapshotPath(path)
		if err != nil {
			return nil, nil, err
		}
		restores = append(restores, restore)
	}
	passwd, err := os.ReadFile(p.path("passwd"))
	if err != nil {
		return nil, nil, err
	}
	oldHash := ""
	for _, line := range strings.Split(string(passwd), "\n") {
		if strings.HasPrefix(line, t.TaskID+":") {
			oldHash = strings.TrimPrefix(line, t.TaskID+":")
		}
	}
	connected, err := p.Docker.NetworkConnected(t.Network, p.name())
	if err != nil {
		return nil, nil, err
	}
	return p, func(ctx context.Context) error {
		p.Docker = p.Docker.WithContext(ctx)
		unlock, err := p.lock()
		if err != nil {
			return err
		}
		defer unlock()
		var failures []error
		for _, restore := range restores {
			if err := restore(); err != nil {
				failures = append(failures, err)
			}
		}
		// Read the current global passwd under its lock. Restoring a whole snapshot
		// here could erase another Task admitted since this attempt began.
		current, err := os.ReadFile(p.path("passwd"))
		if err != nil {
			failures = append(failures, err)
		} else if err := fsutil.AtomicWrite(p.path("passwd"), []byte(upsertPasswd(string(current), t.TaskID, oldHash)), 0644); err != nil {
			failures = append(failures, err)
		}
		if !connected {
			if err := p.Docker.NetworkDisconnect(t.Network, p.name()); err != nil {
				failures = append(failures, err)
			}
		}
		state, found, err := p.Docker.Inspect(p.name())
		if err != nil {
			failures = append(failures, err)
		} else if !found {
			failures = append(failures, fmt.Errorf("shared proxy disappeared during rollback"))
		} else if state.Running {
			if err := p.reconfigure(); err != nil {
				failures = append(failures, err)
			}
		}
		return errors.Join(failures...)
	}, nil
}
