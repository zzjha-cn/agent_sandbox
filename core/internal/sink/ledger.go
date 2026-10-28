package sink

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

const maxLedgerBytes = 1 << 20

// The target lock inode also retains key bindings across tasks and target
// replacement/deletion. Never unlink or replace it. A torn append denies new
// effects until reconciled; target receipts remain queryable independently.
type keyBinding struct {
	Key    string `json:"key"`
	Object string `json:"object"`
	Target string `json:"target"`
	SHA256 string `json:"sha256"`
}

func (b keyBinding) checksum() string {
	b.SHA256 = ""
	payload, _ := json.Marshal(b)
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func bindDelivery(f *os.File, r Receipt) error {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	raw, err := io.ReadAll(io.LimitReader(f, maxLedgerBytes+1))
	if err != nil {
		return err
	}
	if len(raw) > maxLedgerBytes || len(raw) > 0 && raw[len(raw)-1] != '\n' {
		return errors.New("delivery key ledger requires reconciliation")
	}
	matched := false
	for _, line := range bytes.Split(raw, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		var old keyBinding
		if err := json.Unmarshal(line, &old); err != nil || old.SHA256 != old.checksum() {
			return errors.New("delivery key ledger is invalid")
		}
		if old.Key == r.Key {
			if old.Object != r.ArtifactDigest || old.Target != r.TargetBinding {
				return errors.New("delivery key is already bound to another artifact or target")
			}
			matched = true
		}
	}
	if matched {
		return nil
	}
	next := keyBinding{Key: r.Key, Object: r.ArtifactDigest, Target: r.TargetBinding}
	next.SHA256 = next.checksum()
	line, _ := json.Marshal(next)
	line = append(line, '\n')
	if len(raw)+len(line) > maxLedgerBytes {
		return errors.New("delivery key ledger exceeds 1 MiB; preserve it and register a new target")
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		return err
	}
	return errors.Join(f.Sync(), syncDir(filepath.Dir(f.Name())))
}
