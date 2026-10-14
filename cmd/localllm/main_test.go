package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"localllm/internal/changereport"
	"localllm/internal/deps"
	"localllm/internal/docker"
	"localllm/internal/opencode"
	"localllm/internal/pi"
	"localllm/internal/prepgate"
	"localllm/internal/projectimage"
	"localllm/internal/sandbox"
)

// TestSessionMountsNoHostAgentConfig builds a session for each agent against
// a fake HOME that has host agent config and state, and checks none of it is
// mounted.
func TestSessionMountsNoHostAgentConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	hostDirs := []string{".config/opencode", ".local/share/opencode", ".local/state/opencode", ".opencode", ".pi"}
	for _, d := range append(hostDirs, "Projects/app") {
		if err := os.MkdirAll(filepath.Join(home, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	home, _ = filepath.EvalSymlinks(home)

	cases := []struct {
		agent   string
		configs []string
		env     map[string]string
		command []string
	}{
		{
			agent:   opencode.Agent,
			configs: []string{opencodeConfigPath},
			env:     map[string]string{"OPENCODE_DISABLE_PROJECT_CONFIG": "1"},
			command: []string{"opencode"},
		},
		{
			agent:   pi.Agent,
			configs: []string{piAgentDir + "/settings.json", piAgentDir + "/models.json"},
			env:     map[string]string{"PI_CODING_AGENT_DIR": piAgentDir, "PI_CODING_AGENT_SESSION_DIR": piSessionDir, "PI_OFFLINE": "1"},
			command: []string{"pi"},
		},
	}
	for _, c := range cases {
		t.Run(c.agent, func(t *testing.T) {
			sess, err := prepareSession(sessionArgs{
				agent:      c.agent,
				gatewayURL: defaultGateway,
				dir:        filepath.Join(home, "Projects/app"),
			}, sandbox.ModeRun)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.cleanup)

			agent := sess.spec.Services[sandbox.AgentService]
			configs := map[string]bool{}
			var state bool
			for _, v := range agent.Volumes {
				for _, d := range hostDirs {
					if v.Source == filepath.Join(home, d) || strings.HasPrefix(v.Source, filepath.Join(home, d)+"/") {
						t.Errorf("host agent directory mounted: %+v", v)
					}
				}
				switch {
				case slices.Contains(c.configs, v.Target):
					configs[v.Target] = true
					if !v.ReadOnly || !strings.HasPrefix(v.Source, sess.sessionDir+"/") {
						t.Errorf("agent config mount = %+v, want read-only from the session dir", v)
					}
				case v.Target == sandbox.StateDir:
					state = true
					if v.Type != "volume" {
						t.Errorf("state mount = %+v, want a named volume", v)
					}
				}
			}
			if len(configs) != len(c.configs) || !state {
				t.Errorf("configs mounted = %v, state mounted = %v in %+v", configs, state, agent.Volumes)
			}
			for _, cfg := range c.configs {
				if !slices.Contains(sess.mounts.ReadOnly, cfg) {
					t.Errorf("probes don't check %s is read-only", cfg)
				}
			}
			want := sandbox.StateVolume(filepath.Join(home, "Projects/app"), c.agent)
			if got := sess.spec.Volumes["state"].Name; got != want {
				t.Errorf("state volume = %q, want %q", got, want)
			}
			for k, v := range c.env {
				if agent.Environment[k] != v {
					t.Errorf("env %s = %q, want %q", k, agent.Environment[k], v)
				}
			}
			if !slices.Equal(agent.Command, c.command) {
				t.Errorf("command = %q, want %q", agent.Command, c.command)
			}
		})
	}
}

// TestGatewayKeyReachesSessionByNameOnly checks a configured key is passed
// to the agent container by name and isn't written to the session dir.
func TestGatewayKeyReachesSessionByNameOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	const secret = "sk-unit-test-secret"
	t.Setenv("LOCALLLM_GATEWAY_KEY", secret)
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	project := filepath.Join(home, "Projects/app")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}

	sess, err := prepareSession(sessionArgs{agent: defaultAgent, gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	if sess.gatewayKey != secret {
		t.Errorf("session key not loaded")
	}
	agent := sess.spec.Services[sandbox.AgentService]
	if !slices.Contains(agent.PassEnv, sandbox.GatewayKeyEnv) {
		t.Errorf("pass env = %v, want %s", agent.PassEnv, sandbox.GatewayKeyEnv)
	}
	data, err := json.Marshal(sess.spec.Literal())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) {
		t.Error("key in the compose spec")
	}
	var out strings.Builder
	printMounts(&out, sess.spec)
	if strings.Contains(out.String(), secret) || !strings.Contains(out.String(), "gateway key: configured") {
		t.Errorf("check output = %q", out.String())
	}
}

// fakeHome points HOME at a temp dir with a project under ~/Projects, and
// returns the resolved project path.
func fakeHome(t *testing.T, files map[string]string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	t.Setenv("LOCALLLM_ALLOWED_ROOT", "")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	project := filepath.Join(home, "Projects/app")
	for rel, content := range files {
		p := filepath.Join(project, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if out, err := exec.Command("git", "init", "-q", project).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	project, _ = filepath.EvalSymlinks(project)
	return project
}

// TestPrepSession checks prep gets the same protections as run, the
// dependency volumes, the install script, and no key or agent state.
func TestPrepSession(t *testing.T) {
	project := fakeHome(t, map[string]string{
		"package.json":   "{}",
		"pnpm-lock.yaml": "",
		".env":           "SECRET=1\n",
	})
	t.Setenv("LOCALLLM_GATEWAY_KEY", "sk-unit-test-secret")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")

	sess, err := prepareSession(sessionArgs{dir: project}, sandbox.ModePrep)
	if err != nil {
		t.Fatal(err)
	}
	if sess.gatewayKey != "" {
		t.Error("prep loaded the gateway key")
	}
	spec := sess.spec
	agent := spec.Services[sandbox.AgentService]
	if len(agent.PassEnv) > 0 {
		t.Errorf("pass env = %v", agent.PassEnv)
	}
	if _, ok := spec.Services[sandbox.ForwarderService]; ok {
		t.Error("prep has a forwarder")
	}
	readOnly := map[string]bool{}
	var depVolume string
	for _, v := range agent.Volumes {
		if v.ReadOnly {
			readOnly[v.Target] = true
		}
		if v.Target == sandbox.Workspace+"/node_modules" {
			depVolume = spec.Volumes[v.Source].Name
		}
		if v.Target == sandbox.StateDir {
			t.Errorf("prep mounts agent state: %+v", v)
		}
	}
	for _, target := range []string{"/workspace/.env", "/workspace/.git/hooks", "/workspace/.git/config", "/workspace/.envrc", "/workspace/localllm.yaml"} {
		if !readOnly[target] {
			t.Errorf("%s is not read-only in prep", target)
		}
	}
	if depVolume != sandbox.DepVolume(project, "node_modules") {
		t.Errorf("node_modules volume = %q", depVolume)
	}
	if len(agent.Command) != 3 || !strings.Contains(agent.Command[2], "pnpm install --offline --frozen-lockfile --store-dir "+sandbox.PnpmStore) {
		t.Errorf("command = %q", agent.Command)
	}

	// The fetch phase runs only the fetch commands, with the same
	// protections and no key.
	fetch := sess.fetchSpec.Services[sandbox.AgentService]
	if len(fetch.Command) != 3 || !strings.Contains(fetch.Command[2], "pnpm fetch --ignore-pnpmfile") || strings.Contains(fetch.Command[2], "pnpm install") {
		t.Errorf("fetch command = %q", fetch.Command)
	}
	if len(fetch.PassEnv) > 0 {
		t.Errorf("fetch pass env = %v", fetch.PassEnv)
	}
	fetchRO := map[string]bool{}
	for _, v := range fetch.Volumes {
		fetchRO[v.Target] = v.ReadOnly
	}
	for _, target := range []string{"/workspace/.env", "/workspace/.git/hooks", "/workspace/.git/config", "/workspace/localllm.yaml"} {
		if !fetchRO[target] {
			t.Errorf("%s is not read-only in fetch", target)
		}
	}
	if ro, ok := fetchRO[sandbox.Workspace+"/node_modules"]; !ok || ro {
		t.Error("fetch lacks the writable dependency volume, so pnpm fetch would write to the host")
	}

	// Docker creates the mount point; cleanup removes it while empty.
	nm := filepath.Join(project, "node_modules")
	if err := os.Mkdir(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	sess.cleanup()
	if _, err := os.Lstat(nm); !os.IsNotExist(err) {
		t.Errorf("node_modules mount point left behind: %v", err)
	}
}

// TestHostDependencyDirKept checks cleanup never removes a dependency
// directory the host already had.
func TestHostDependencyDirKept(t *testing.T) {
	project := fakeHome(t, map[string]string{"package.json": "{}"})
	nm := filepath.Join(project, "node_modules")
	if err := os.Mkdir(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := prepareSession(sessionArgs{dir: project}, sandbox.ModePrep)
	if err != nil {
		t.Fatal(err)
	}
	sess.cleanup()
	if _, err := os.Stat(nm); err != nil {
		t.Errorf("host node_modules removed: %v", err)
	}
}

func TestPrepRefusesVolumeOverProtectedPath(t *testing.T) {
	project := fakeHome(t, map[string]string{deps.ConfigFile: "prep:\n  dirs: [.husky]\n", ".husky/_/h": ""})
	if out, err := exec.Command("git", "-C", project, "config", "core.hooksPath", ".husky/_").CombinedOutput(); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if _, err := prepareSession(sessionArgs{dir: project}, sandbox.ModePrep); err == nil || !strings.Contains(err.Error(), ".husky") {
		t.Errorf("err = %v, want refusal of the volume over .husky", err)
	}
}

func TestPrepGateOutput(t *testing.T) {
	sess := &session{project: "/p", deps: deps.Plan{
		Steps: []deps.Step{{Name: "pnpm", Fetch: "pnpm fetch", Script: "pnpm install"}},
		Hooks: []string{"package.json: postinstall: node setup.js"},
	}}
	var first strings.Builder
	printPrepGate(&first, sess, prepgate.Decision{First: true})
	for _, want := range []string{"First prep of /p", "pnpm: pnpm fetch", "pnpm: pnpm install", "postinstall: node setup.js"} {
		if !strings.Contains(first.String(), want) {
			t.Errorf("first prep output lacks %q:\n%s", want, first.String())
		}
	}

	var others []changereport.Change
	for i := range 25 {
		others = append(others, changereport.Change{Path: fmt.Sprintf("src/f%02d.js", i), Kind: changereport.Modified})
	}
	var changed strings.Builder
	printPrepGate(&changed, sess, prepgate.Decision{
		Inputs: []changereport.Change{{Path: "package.json", Kind: changereport.Modified}},
		Others: others,
	})
	for _, want := range []string{"modified  package.json", "25 other file(s)", "src/f19.js", "... and 5 more", "git -C /p diff"} {
		if !strings.Contains(changed.String(), want) {
			t.Errorf("changed output lacks %q:\n%s", want, changed.String())
		}
	}
	if strings.Contains(changed.String(), "src/f20.js") {
		t.Errorf("output not truncated:\n%s", changed.String())
	}
}

func TestReadYes(t *testing.T) {
	for in, want := range map[string]bool{"y\n": true, "Yes\n": true, " y ": true, "n\n": false, "": false, "yy\n": false} {
		if got := readYes(strings.NewReader(in)); got != want {
			t.Errorf("readYes(%q) = %v", in, got)
		}
	}
}

func TestUnknownAgentRefused(t *testing.T) {
	_, err := prepareSession(sessionArgs{agent: "claude", gatewayURL: defaultGateway, dir: t.TempDir()}, sandbox.ModeRun)
	if err == nil || !strings.Contains(err.Error(), "opencode, pi") {
		t.Errorf("err = %v, want unknown agent listing opencode, pi", err)
	}
}

func TestParseSessionArgs(t *testing.T) {
	cases := []struct {
		args    []string
		dir     string
		model   string
		agent   string
		ro      []string
		command []string
		wantErr bool
	}{
		{args: nil, dir: "."},
		{args: []string{"app"}, dir: "app"},
		{args: []string{"--model", "m", "app"}, dir: "app", model: "m"},
		{args: []string{"app", "--model", "m"}, dir: "app", model: "m"},
		{args: []string{"--ro", "a", "app", "--ro", "b"}, dir: "app", ro: []string{"a", "b"}},
		{args: []string{"app", "--", "sh", "-c", "x"}, dir: "app", command: []string{"sh", "-c", "x"}},
		{args: []string{"--", "opencode", "run", "hi"}, dir: ".", command: []string{"opencode", "run", "hi"}},
		{args: []string{"--model", "m", "--", "app"}, dir: ".", model: "m", command: []string{"app"}},
		{args: []string{"app", "--agent", "pi"}, dir: "app", agent: "pi"},
		{args: []string{"a", "b"}, wantErr: true},
		{args: []string{"--nope"}, wantErr: true},
	}
	for _, c := range cases {
		sa, err := parseSessionArgs("run", c.args, nil)
		if c.wantErr {
			if err == nil {
				t.Errorf("%q: no error", c.args)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if c.agent == "" {
			c.agent = defaultAgent
		}
		if sa.agent != c.agent || sa.dir != c.dir || sa.model != c.model || !slices.Equal(sa.ro, c.ro) || !slices.Equal(sa.command, c.command) {
			t.Errorf("%q: got agent=%q dir=%q model=%q ro=%q command=%q", c.args, sa.agent, sa.dir, sa.model, sa.ro, sa.command)
		}
	}
}

// TestSessionImage checks which image prepareSession gives the agent and
// the volumes service, with Docker's images faked.
func TestSessionImage(t *testing.T) {
	const dockerfile = "FROM localllm-base:latest\nRUN apt-get install -y tree\n"
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	cases := []struct {
		name    string
		files   map[string]string
		built   bool
		labels  string // Dockerfile the fake image was built from
		image   string // "project" or the base image
		warning string
	}{
		{name: "no Dockerfile", files: map[string]string{"package.json": "{}"}, image: baseImage},
		{name: "built", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, built: true, labels: dockerfile, image: "project"},
		{name: "not built", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, image: baseImage, warning: "has no image yet"},
		{name: "stale", files: map[string]string{projectimage.File: dockerfile, "package.json": "{}"}, built: true, labels: "FROM localllm-base:latest\n", image: "project", warning: "changed since"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := fakeHome(t, c.files)
			calls := 0
			inspectImage = func(tag string) (string, map[string]string, bool, error) {
				calls++
				switch {
				case tag == baseImage:
					return "sha256:base", nil, true, nil
				case tag == projectimage.Tag(project) && c.built:
					return "sha256:project", projectimage.Labels([]byte(c.labels), "sha256:base"), true, nil
				}
				return "", nil, false, nil
			}
			t.Cleanup(func() { inspectImage = docker.InspectImage })

			want := c.image
			if want == "project" {
				want = projectimage.Tag(project)
			}
			for _, mode := range []sandbox.Mode{sandbox.ModeRun, sandbox.ModePrep} {
				sess, err := prepareSession(sessionArgs{agent: opencode.Agent, gatewayURL: defaultGateway, dir: project}, mode)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(sess.cleanup)
				specs := []sandbox.Compose{sess.spec}
				if mode == sandbox.ModePrep {
					specs = append(specs, sess.fetchSpec)
				}
				for _, spec := range specs {
					if got := spec.Services[sandbox.AgentService].Image; got != want {
						t.Errorf("mode %d: agent image = %s, want %s", mode, got, want)
					}
					if got := spec.Services[sandbox.VolumesService].Image; got != baseImage {
						t.Errorf("mode %d: volumes service image = %s, want the base image", mode, got)
					}
				}
				var out strings.Builder
				sess.printWarnings(&out)
				if c.warning == "" && strings.Contains(out.String(), "WARNING") {
					t.Errorf("mode %d: unexpected warning: %s", mode, out.String())
				}
				if c.warning != "" && (!strings.Contains(out.String(), c.warning) || !strings.Contains(out.String(), "localllm build "+project)) {
					t.Errorf("mode %d: output = %q, want %q and the build command", mode, out.String(), c.warning)
				}
			}
			if c.files[projectimage.File] == "" && calls > 0 {
				t.Error("inspected images for a project without a Dockerfile")
			}
		})
	}
}

// TestDockerfileReadOnly checks the project Dockerfile is protected like
// localllm.yaml, since a build runs it with network access.
func TestDockerfileReadOnly(t *testing.T) {
	project := fakeHome(t, map[string]string{})
	t.Setenv("LOCALLLM_GATEWAY_KEY", "")
	t.Setenv("LOCALLLM_GATEWAY_KEY_FILE", "")
	sess, err := prepareSession(sessionArgs{agent: opencode.Agent, gatewayURL: defaultGateway, dir: project}, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sess.cleanup)
	if !slices.Contains(sess.mounts.ReadOnly, sandbox.Workspace+"/"+projectimage.File) {
		t.Errorf("%s is not among the probed read-only mounts %v", projectimage.File, sess.mounts.ReadOnly)
	}
}
