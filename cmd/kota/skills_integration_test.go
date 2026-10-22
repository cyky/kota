//go:build integration

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"kota/internal/docker"
	"kota/internal/projectskills"
	"kota/internal/sandbox"
)

// Each skill folder kota could load has its own description, so a request
// shows which of them the agent loaded.
const (
	agentsSkill   = "AGENTS-SKILL-MARKER"
	opencodeSkill = "OPENCODE-SKILL-MARKER"
	claudeSkill   = "CLAUDE-SKILL-MARKER"
	piSkill       = "PI-SKILL-MARKER"
)

// skillsProject makes a git project under a fresh allowed root with a skill
// in .agents/skills and one in each folder that must not load, and kota.yaml
// set to config ("" for none).
func skillsProject(t *testing.T, config string) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp(cache, "kota-test-")
	if err != nil {
		t.Fatal(err)
	}
	root, _ = filepath.EvalSymlinks(root)
	t.Cleanup(func() { os.RemoveAll(root) })
	t.Setenv("KOTA_ALLOWED_ROOT", root)
	project := filepath.Join(root, "app")
	files := map[string]string{
		"README.md": "app\n",
		projectskills.Dir + "/kota-test/SKILL.md": skillFile("kota-test", agentsSkill),
		".opencode/skills/oc/SKILL.md":            skillFile("oc", opencodeSkill),
		".claude/skills/cl/SKILL.md":              skillFile("cl", claudeSkill),
		".pi/skills/ps/SKILL.md":                  skillFile("ps", piSkill),
	}
	if config != "" {
		files[projectskills.ConfigFile] = config
	}
	for rel, content := range files {
		p := filepath.Join(project, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	hostGit(t, project, "init", "-q", "-b", "main")
	t.Cleanup(func() {
		if available, err := docker.ListVolumes(); err == nil {
			docker.RemoveVolumes(sandbox.ProjectVolumes(project, available)...)
		}
	})
	return project
}

func skillFile(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + " use this skill for kota tests\n---\n\nSay hello.\n"
}

// agentPrompt runs the agent once on a prompt, without its terminal UI.
var agentPrompt = map[string]string{
	"opencode": "timeout 120 opencode run --standalone 'say hello' </dev/null",
	"pi":       "timeout 120 pi -p 'say hello' </dev/null",
}

// TestProjectSkills runs each agent in a shell Session against a scripted
// gateway, and checks the Project skills reach the model only when
// kota.yaml opts in, never the project's .opencode, .claude or .pi
// skills, and that the agent can write .agents/skills only with
// --skills-writable.
func TestProjectSkills(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")

	cases := []struct {
		name, agent, config string
		writable            bool
		loaded              bool
	}{
		{name: "opencode", agent: "opencode", config: "skills: true\n", loaded: true},
		{name: "pi", agent: "pi", config: "skills: true\n", loaded: true},
		{name: "opencode without opt-in", agent: "opencode"},
		{name: "pi without opt-in", agent: "pi"},
		{name: "pi writable", agent: "pi", config: "skills: true\n", writable: true, loaded: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			project := skillsProject(t, c.config)
			g := &scriptedGateway{}
			sa := sessionArgs{agent: c.agent, gatewayURL: g.start(t), dir: project, shell: true, skillsWritable: c.writable}
			var out string
			var err error
			withSession(t, sa, func(*sandbox.Compose) {}, func(sess *session, ds *docker.Session) {
				out, err = ds.Capture("bash", "-c", `exec 2>&1
cd /workspace
`+agentPrompt[c.agent]+`
echo "AGENT-EXIT=$?"
echo x >> .agents/skills/kota-test/SKILL.md && echo WROTE
mkdir .agents/skills/planted && echo PLANTED
true`)
				if err != nil {
					t.Fatalf("%v\n%s", err, out)
				}
			})
			t.Logf("inside:\n%s", out)
			g.mu.Lock()
			defer g.mu.Unlock()
			if len(g.bodies) == 0 {
				t.Fatalf("the agent sent no chat request:\n%s", out)
			}
			all := strings.Join(g.bodies, "\n")
			if got := strings.Contains(all, agentsSkill); got != c.loaded {
				t.Errorf(".agents/skills reached the model: %v, want %v", got, c.loaded)
			}
			for _, other := range []string{opencodeSkill, claudeSkill, piSkill} {
				if strings.Contains(all, other) {
					t.Errorf("a skill outside .agents/skills reached the model: %s", other)
				}
			}
			// Without the opt-in the folder is ordinary project files.
			wantWrite := c.writable || c.config == ""
			for _, w := range []string{"WROTE", "PLANTED"} {
				if got := strings.Contains(out, w); got != wantWrite {
					t.Errorf("%s: %v, want %v", w, got, wantWrite)
				}
			}
		})
	}
}

// TestProjectSkillsNoHistory checks a --no-history Session loads the skills
// from the working tree, uncommitted ones included, read-only, and that
// they don't count as the Session's uncommitted work.
func TestProjectSkillsNoHistory(t *testing.T) {
	requireBaseImage(t)
	config := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(config, "gitconfig"))
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")

	project := skillsProject(t, "skills: true\n")
	hostGit(t, project, "config", "user.name", "t")
	hostGit(t, project, "config", "user.email", "t@example.com")
	hostGit(t, project, "add", "README.md", projectskills.Dir)
	hostGit(t, project, "commit", "-q", "-m", "start")
	// Edited and added since the commit, and kota.yaml untracked.
	skill := filepath.Join(project, filepath.FromSlash(projectskills.Dir), "kota-test", "SKILL.md")
	if err := os.WriteFile(skill, []byte(skillFile("kota-test", agentsSkill+"-UNCOMMITTED")), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(project, filepath.FromSlash(projectskills.Dir), "new"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, filepath.FromSlash(projectskills.Dir), "new", "SKILL.md"), []byte(skillFile("new", "NEW-SKILL")), 0o644); err != nil {
		t.Fatal(err)
	}
	// The other skill folders aren't kota's: commit them away.
	hostGit(t, project, "add", ".opencode", ".claude", ".pi")
	hostGit(t, project, "commit", "-q", "-m", "other agents")

	g := &scriptedGateway{}
	run := noHistorySession(t, project, g.start(t), `
cat .agents/skills/kota-test/SKILL.md .agents/skills/new/SKILL.md
`+agentPrompt["opencode"]+`
echo x >> .agents/skills/kota-test/SKILL.md && echo WROTE
git add -A && git status --porcelain
true`)
	for _, want := range []string{agentsSkill + "-UNCOMMITTED", "NEW-SKILL"} {
		if !strings.Contains(run.inside, want) {
			t.Errorf("the copy lacks %q", want)
		}
	}
	if strings.Contains(run.inside, "WROTE") {
		t.Error("the copy's skills are writable")
	}
	if strings.Contains(run.inside, "new/SKILL.md") {
		t.Error("git add -A staged the copied skills")
	}
	g.mu.Lock()
	all := strings.Join(g.bodies, "\n")
	g.mu.Unlock()
	if !strings.Contains(all, "NEW-SKILL") {
		t.Error("the uncommitted skill didn't reach the model")
	}
	// Nothing of the Session's own is left, so the copy goes.
	if !strings.Contains(run.report, "the copy was removed") {
		t.Errorf("the copied skills kept the copy:\n%s", run.report)
	}
}
