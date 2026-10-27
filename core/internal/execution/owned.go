package execution

import (
	"context"
	"fmt"

	"sandx/internal/docker"
)

// ownedCreation registers rollback before a daemon mutation. A unique label,
// rather than the resource name, distinguishes a partial creation from a
// preexisting object or a concurrent creator. The task lock serializes sbx
// mutations; these labels are not a security boundary against Docker API users.
func ownedCreation(d *docker.Client, u *Undo, kind, name string, labels map[string]string) map[string]string {
	token := newAttemptID()
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out["sbx.provision"] = token
	u.Add(kind, name, true, func(ctx context.Context) error {
		client := d.WithContext(ctx)
		items, err := client.ListByLabel(kind, "sbx.provision="+token)
		if err != nil {
			return err
		}
		for _, item := range items {
			field := "Name"
			if kind == "container" {
				field = "Names"
			}
			if item.Str(field) != name {
				continue
			}
			target := item.Str("ID")
			if target == "" {
				target = name
			}
			switch kind {
			case "container":
				err = client.Rm(target)
			case "network":
				err = client.NetworkRm(target)
			case "volume":
				err = client.VolumeRm(name)
			default:
				return fmt.Errorf("unknown owned resource kind %q", kind)
			}
			if err != nil && !docker.IsNotFound(err) {
				return err
			}
		}
		return nil
	})
	return out
}

// CreateOwnedContainer also captures a container created before run returns an error.
func CreateOwnedContainer(d *docker.Client, spec docker.RunSpec, u *Undo) error {
	spec.Labels = ownedCreation(d, u, "container", spec.Name, spec.Labels)
	_, err := d.RunContainer(spec)
	return err
}

func CreateOwnedNetwork(d *docker.Client, name string, internal bool, labels map[string]string, u *Undo) error {
	exists, err := d.NetworkExists(name)
	if err != nil || exists {
		return err
	}
	return d.NetworkCreate(name, internal, ownedCreation(d, u, "network", name, labels))
}

// CreateOwnedVolume returns true only when the daemon's labels confirm that this
// attempt created the volume. Docker volume create may return an existing volume.
func CreateOwnedVolume(d *docker.Client, name string, labels map[string]string, u *Undo) (bool, error) {
	labels = ownedCreation(d, u, "volume", name, labels)
	created, err := d.VolumeCreate(name, labels)
	if err != nil || !created {
		return false, err
	}
	actual, exists, err := d.VolumeLabels(name)
	if err != nil {
		return false, err
	}
	if !exists {
		return false, fmt.Errorf("volume disappeared after creation: %s", name)
	}
	return actual["sbx.provision"] == labels["sbx.provision"], nil
}
