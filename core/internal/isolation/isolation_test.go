package isolation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sandx/internal/docker"
)

// A subprocess fixture implements metadata operations only. Any container run,
// mount, removal or unknown command fails, making hidden mutations observable.
func init() {
	if dir := os.Getenv("SBX_ISOLATION_HELPER"); dir != "" {
		args := os.Args[1:]
		f, _ := os.OpenFile(filepath.Join(dir, "commands"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		b, _ := json.Marshal(args)
		fmt.Fprintln(f, string(b))
		f.Close()
		if len(args) >= 1 && args[0] == "info" {
			if os.Getenv("SBX_HELPER_FAIL") == "1" {
				os.Exit(9)
			}
			fmt.Println(os.Getenv("SBX_HELPER_DAEMON"))
			os.Exit(0)
		}
		if len(args) >= 2 && args[0] == "volume" {
			name := args[len(args)-1]
			path := filepath.Join(dir, name+".json")
			switch args[1] {
			case "inspect":
				data, err := os.ReadFile(path)
				if os.IsNotExist(err) {
					fmt.Fprintln(os.Stderr, "No such volume")
					os.Exit(1)
				}
				if err != nil {
					os.Exit(8)
				}
				if args[3] == "{{.Name}}" {
					fmt.Println(name)
				} else {
					fmt.Println(string(data))
				}
				os.Exit(0)
			case "create":
				if _, err := os.Stat(path); os.IsNotExist(err) {
					labels := map[string]string{}
					for i := 2; i < len(args)-1; i++ {
						if args[i] == "--label" {
							i++
							k, v, _ := strings.Cut(args[i], "=")
							labels[k] = v
						}
					}
					if winner := os.Getenv("SBX_HELPER_RACE_OWNER"); winner != "" {
						labels["sbx.owner"] = winner
					}
					b, _ := json.Marshal(labels)
					os.WriteFile(path, b, 0o600)
				}
				fmt.Println(name)
				os.Exit(0)
			}
		}
		fmt.Fprintln(os.Stderr, "unexpected metadata operation")
		os.Exit(7)
	}
}

func helper(t *testing.T) (*Guard, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SBX_ISOLATION_HELPER", dir)
	t.Setenv("SBX_HELPER_DAEMON", "daemon-a")
	t.Setenv("SBX_HELPER_FAIL", "")
	t.Setenv("SBX_HELPER_RACE_OWNER", "")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return New(&docker.Client{Bin: bin}, Policy{Owner: "operator-a"}), dir
}

func putVolume(t *testing.T, dir, name string, labels map[string]string) {
	t.Helper()
	b, err := json.Marshal(labels)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestCheckIsReadOnlyAndEnsureClaimsEmptyDaemon(t *testing.T) {
	g, dir := helper(t)
	s, err := g.Status(context.Background())
	if err != nil || !s.Allowed || s.Claimed || s.Namespace != "daemon-"+hash("daemon-a") {
		t.Fatalf("%+v %v", s, err)
	}
	if _, err := os.Stat(filepath.Join(dir, MarkerVolume+".json")); !os.IsNotExist(err) {
		t.Fatal("read-only check created a volume")
	}
	s, err = g.Ensure(context.Background())
	if err != nil || !s.Claimed {
		t.Fatalf("%+v %v", s, err)
	}
	first, _ := os.ReadFile(filepath.Join(dir, MarkerVolume+".json"))
	if _, err := g.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, MarkerVolume+".json"))
	if string(first) != string(second) {
		t.Fatal("repeated ensure modified ownership")
	}
}

func TestLegacyRequiresExplicitAdoptionWithoutChangingVolumes(t *testing.T) {
	g, dir := helper(t)
	putVolume(t, dir, "sbx-home", map[string]string{"sbx.kind": "shared"})
	before, _ := os.ReadFile(filepath.Join(dir, "sbx-home.json"))
	if err := g.Check(context.Background()); !IsRejection(err) {
		t.Fatalf("legacy admission accepted: %v", err)
	}
	if _, err := g.Ensure(context.Background()); !IsRejection(err) {
		t.Fatalf("implicit adoption: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, MarkerVolume+".json")); !os.IsNotExist(err) {
		t.Fatal("legacy check created ownership")
	}
	s, err := g.Adopt(context.Background())
	if err != nil || !s.Allowed || !s.Claimed || !reflect.DeepEqual(s.LegacyVolumes, []string{"sbx-home"}) {
		t.Fatalf("%+v %v", s, err)
	}
	after, _ := os.ReadFile(filepath.Join(dir, "sbx-home.json"))
	if string(before) != string(after) {
		t.Fatal("adoption changed old volume labels")
	}
	commands, _ := os.ReadFile(filepath.Join(dir, "commands"))
	for _, line := range strings.Split(strings.TrimSpace(string(commands)), "\n") {
		var args []string
		if err := json.Unmarshal([]byte(line), &args); err != nil {
			t.Fatal(err)
		}
		if args[0] != "info" && !(args[0] == "volume" && (args[1] == "inspect" || args[1] == "create")) {
			t.Fatal("credential contents accessed", args)
		}
	}
}

func TestForeignOwnerCannotCheckEnsureOrAdopt(t *testing.T) {
	g, dir := helper(t)
	if _, err := g.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, MarkerVolume+".json"))
	g.Policy.Owner = "operator-b"
	for _, action := range []func(context.Context) (Status, error){g.Status, g.Ensure, g.Adopt} {
		if _, err := action(context.Background()); !IsRejection(err) {
			t.Fatalf("foreign operator accepted: %v", err)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, MarkerVolume+".json"))
	if string(before) != string(after) {
		t.Fatal("ownership overwritten")
	}
}

func TestConcurrentClaimVerifiesActualLabels(t *testing.T) {
	g, dir := helper(t)
	t.Setenv("SBX_HELPER_RACE_OWNER", "winner-b")
	if _, err := g.Ensure(context.Background()); !IsRejection(err) {
		t.Fatalf("lost claim race was accepted: %v", err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, MarkerVolume+".json"))
	if !strings.Contains(string(b), "winner-b") {
		t.Fatal("winner's record was removed/overwritten")
	}
}

func TestDaemonIdentityChangeRejectsAndOriginalIdentityRecovers(t *testing.T) {
	g, _ := helper(t)
	if _, err := g.Ensure(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBX_HELPER_DAEMON", "daemon-b")
	if err := g.Check(context.Background()); !IsRejection(err) {
		t.Fatal("restored foreign daemon identity accepted", err)
	}
	t.Setenv("SBX_HELPER_DAEMON", "daemon-a")
	if err := g.Check(context.Background()); err != nil {
		t.Fatal("original daemon could not recover", err)
	}
}

func TestCorruptOrForeignVolumeLabelsFailClosed(t *testing.T) {
	for _, marker := range []bool{false, true} {
		t.Run(fmt.Sprint(marker), func(t *testing.T) {
			g, dir := helper(t)
			name := "sbx-home"
			if marker {
				name = MarkerVolume
			}
			putVolume(t, dir, name, map[string]string{"sbx.owner": "operator-a"})
			if _, err := g.Adopt(context.Background()); !IsRejection(err) {
				t.Fatal("partial labels accepted", err)
			}
		})
	}
}

func TestPolicyRejectsSharedDaemonBeforeDocker(t *testing.T) {
	g := New(&docker.Client{Bin: "/does-not-exist"}, Policy{Boundary: "shared-daemon"})
	if _, err := g.Ensure(context.Background()); !IsRejection(err) || !strings.Contains(err.Error(), "shared-daemon") {
		t.Fatal(err)
	}
}

func TestDockerFailureAndCancellationAreNotPolicyRejections(t *testing.T) {
	g, _ := helper(t)
	t.Setenv("SBX_HELPER_FAIL", "1")
	if err := g.Check(context.Background()); err == nil || IsRejection(err) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := g.Check(ctx); err == nil || IsRejection(err) {
		t.Fatal(err)
	}
}

func TestLocalOwnerIndependentOfSBXHome(t *testing.T) {
	a, err := LocalOwner()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SBX_HOME", t.TempDir())
	b, err := LocalOwner()
	if err != nil || a != b {
		t.Fatalf("%s %s %v", a, b, err)
	}
}
