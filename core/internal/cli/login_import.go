package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const maxAuthImportBytes = 1 << 20

// readCodexAuth reads only the explicitly selected auth file. Never walk the
// user's configuration directory or include secret contents in an error.
func readCodexAuth(source string) ([]byte, error) {
	path, err := filepath.Abs(expandHome(source))
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot access Codex import source: %w", err)
	}
	if st.IsDir() {
		path = filepath.Join(path, "auth.json")
	}
	st, err = os.Stat(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("auth.json is missing in the selected directory; a keyring login cannot be imported as a file (use Codex file credential storage)")
	}
	if err != nil {
		return nil, fmt.Errorf("cannot access auth.json: %w", err)
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("Codex auth source must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("cannot open auth.json: %w", err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxAuthImportBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cannot read auth.json")
	}
	if len(data) > maxAuthImportBytes {
		return nil, fmt.Errorf("auth.json exceeds the 1 MiB import limit")
	}
	var auth struct {
		Mode   string `json:"auth_mode"`
		APIKey string `json:"OPENAI_API_KEY"`
		Tokens *struct {
			Access  string `json:"access_token"`
			Refresh string `json:"refresh_token"`
			ID      string `json:"id_token"`
		} `json:"tokens"`
	}
	if json.Unmarshal(data, &auth) != nil {
		return nil, fmt.Errorf("auth.json is not a valid Codex credential object")
	}
	hasKey := strings.TrimSpace(auth.APIKey) != ""
	hasTokens := auth.Tokens != nil && auth.Tokens.Access != "" && auth.Tokens.Refresh != "" && auth.Tokens.ID != ""
	switch auth.Mode {
	case "apikey":
		if !hasKey {
			return nil, fmt.Errorf("auth.json has no API key credentials")
		}
	case "chatgpt":
		if !hasTokens {
			return nil, fmt.Errorf("auth.json has incomplete ChatGPT credentials")
		}
	case "":
		if !hasKey && !hasTokens {
			return nil, fmt.Errorf("auth.json contains no importable credentials; check Codex file credential storage")
		}
	default:
		return nil, fmt.Errorf("auth.json uses an unsupported authentication mode")
	}
	return data, nil
}

// All credentials travel over stdin, never argv, a host bind mount or logs.
// Validate in a private staging home before atomically replacing auth.json.
const codexImportScript = `set -eu
umask 077
dest=/home/agent/.codex
stage=
cleanup() { if [ -n "$stage" ]; then rm -rf "$stage"; fi; }
trap cleanup EXIT
if [ -L "$dest" ] || [ -L "$dest/auth.json" ]; then printf unsafe; exit 22; fi
if [ -e "$dest/auth.json" ] && [ "$1" != force ]; then printf exists; exit 20; fi
mkdir -p "$dest"
stage=$(mktemp -d /home/agent/.sbx-codex-import.XXXXXX)
cat > "$stage/auth.json"
chmod 600 "$stage/auth.json"
if ! CODEX_HOME="$stage" codex -c 'cli_auth_credentials_store="file"' login status >/dev/null 2>&1; then printf invalid; exit 21; fi
if [ "$1" = force ]; then
mv -f "$stage/auth.json" "$dest/"
else
if ! ln "$stage/auth.json" "$dest/" 2>/dev/null; then
if [ -e "$dest/auth.json" ] || [ -L "$dest/auth.json" ]; then printf exists; else printf commit_failed; fi
exit 24
fi
fi
printf imported
`

func codexImportArgs(tag, volume string, force bool) []string {
	mode := "preserve"
	if force {
		mode = "force"
	}
	return []string{"run", "--rm", "-i", "--network", "none", "--mount", "type=volume,source=" + volume + ",target=/home/agent", tag, "bash", "-c", codexImportScript, "sbx-import", mode}
}

func (a *App) importCodexAuth(tag string, data []byte, force bool) error {
	out, err := a.Docker.RunInput(bytes.NewReader(data), codexImportArgs(tag, sharedHome, force)...)
	if err != nil {
		switch out {
		case "exists":
			return fmt.Errorf("Codex credentials already exist in sbx-home; use --force to replace them")
		case "invalid":
			return fmt.Errorf("the container's Codex CLI could not recognize the imported credentials; existing credentials were preserved")
		case "unsafe":
			return fmt.Errorf("the destination Codex directory or auth file is a symlink; import refused")
		default:
			return fmt.Errorf("could not import Codex credentials: %w", err)
		}
	}
	if out != "imported" {
		return fmt.Errorf("Codex import did not confirm installation")
	}
	fmt.Fprintln(a.Out, "Imported Codex credentials into sbx-home; the container CLI recognizes them. Model access has not been tested.")
	return nil
}
