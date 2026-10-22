package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"kota/internal/agent"
	"kota/internal/projectskills"
	"kota/internal/sandbox"
)

// TestProjectSkillsSession checks how run, shell, check and prep treat the
// Project skills: loaded only when kota.yaml opts in, read-only unless
// --skills-writable, and a kota.yaml that can't be parsed doesn't stop the
// session.
func TestProjectSkillsSession(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	const skill = projectskills.Dir + "/deploy/SKILL.md"
	target := sandbox.Workspace + "/" + projectskills.Dir
	cases := []struct {
		name     string
		config   string // "" for no kota.yaml
		writable bool
		mode     sandbox.Mode // ModeRun unless set
		want     projectskills.Mode
		readOnly bool
		warning  string
		wantErr  string
	}{
		{name: "no opt-in", want: projectskills.Off},
		{name: "opted out", config: "skills: false\n", want: projectskills.Off},
		{name: "read-only", config: "skills: true\n", want: projectskills.ReadOnly, readOnly: true},
		{name: "writable", config: "skills: true\n", writable: true, want: projectskills.Writable},
		{name: "writable without opt-in", writable: true, wantErr: "--skills-writable needs skills: true in kota.yaml"},
		{name: "prep", config: "skills: true\n", mode: sandbox.ModePrep, want: projectskills.ReadOnly, readOnly: true},
		{name: "broken config", config: "prep: [\n", want: projectskills.Off, readOnly: true,
			warning: "parse kota.yaml: yaml: line 1: did not find expected node content: project skills are off for this session, and .agents/skills is read-only"},
		{name: "broken config, writable", config: "prep: [\n", writable: true, wantErr: "parse kota.yaml"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{skill: "---\nname: deploy\n---\n"}
			if c.config != "" {
				files[projectskills.ConfigFile] = c.config
			}
			project := fakeHome(t, files)
			var sess *session
			var err error
			if c.mode == sandbox.ModePrep {
				sess, err = prepareTestSession(sessionArgs{dir: project}, c.mode)
			} else {
				// As run does: a broken kota.yaml falls back for the dependencies.
				sess, err = prepareShellSession(sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project, skillsWritable: c.writable})
			}
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(sess.cleanup)
			if sess.skills != c.want {
				t.Errorf("skills = %v, want %v", sess.skills, c.want)
			}
			if got := slices.Contains(sess.expect.ReadOnly, target); got != c.readOnly {
				t.Errorf("%s read-only: %v, want %v (%q)", target, got, c.readOnly, sess.expect.ReadOnly)
			}
			hasWarning := slices.Contains(sess.layout.Warnings, c.warning)
			if c.warning != "" && !hasWarning {
				t.Errorf("warnings = %q, want %q", sess.layout.Warnings, c.warning)
			}
		})
	}
}

// TestProjectSkillsMissingFolder checks a missing .agents/skills is made
// for the session and held read-only itself, so the rest of .agents stays
// writable, and is removed afterwards unless something is in it.
func TestProjectSkillsMissingFolder(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	for _, existing := range []string{"", ".agents/notes.md"} {
		files := map[string]string{projectskills.ConfigFile: "skills: true\n"}
		if existing != "" {
			files[existing] = "x"
		}
		project := fakeHome(t, files)
		sess, err := prepareShellSession(sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project})
		if err != nil {
			t.Fatal(err)
		}
		if info, err := os.Stat(filepath.Join(project, projectskills.Dir)); err != nil || !info.IsDir() {
			t.Errorf("%q: %s not made for the session: %v", existing, projectskills.Dir, err)
		}
		var skillsMount, agentsMount *sandbox.Volume
		for _, v := range sess.spec.Services[sandbox.AgentService].Volumes {
			switch v.Target {
			case sandbox.Workspace + "/" + projectskills.Dir:
				skillsMount = &v
			case sandbox.Workspace + "/.agents":
				agentsMount = &v
			}
		}
		if skillsMount == nil || !skillsMount.ReadOnly || skillsMount.Source != filepath.Join(project, projectskills.Dir) {
			t.Errorf("%q: %s mount = %+v, want it read-only over itself", existing, projectskills.Dir, skillsMount)
		}
		if agentsMount == nil || agentsMount.ReadOnly {
			t.Errorf("%q: .agents mount = %+v, want it pinned, writable", existing, agentsMount)
		}
		sess.cleanup()
		_, err = os.Stat(filepath.Join(project, ".agents"))
		if existing == "" && !os.IsNotExist(err) {
			t.Errorf(".agents made for the session is still there: %v", err)
		}
		if existing != "" && err != nil {
			t.Errorf("the project's .agents was removed: %v", err)
		}
		if _, err := os.Stat(filepath.Join(project, projectskills.Dir)); !os.IsNotExist(err) {
			t.Errorf("%q: %s made for the session is still there: %v", existing, projectskills.Dir, err)
		}
	}

	// A session that fails to prepare removes them too.
	project := fakeHome(t, map[string]string{projectskills.ConfigFile: "skills: true\n"})
	if _, err := prepareShellSession(sessionArgs{agent: "nobody", gatewayURL: defaultGateway, dir: project}); err == nil {
		t.Fatal("unknown agent accepted")
	}
	if _, err := os.Stat(filepath.Join(project, ".agents")); !os.IsNotExist(err) {
		t.Errorf(".agents left after a failed prepare: %v", err)
	}
}

func TestProjectSkillsRefuseLinksOut(t *testing.T) {
	t.Setenv("KOTA_GATEWAY_KEY", "")
	t.Setenv("KOTA_GATEWAY_KEY_FILE", "")
	project := fakeHome(t, map[string]string{
		projectskills.ConfigFile:        "skills: true\n",
		"tools/skills/deploy/SKILL.md":  "x",
		projectskills.Dir + "/.gitkeep": "",
	})
	if err := os.Symlink("../../tools/skills/deploy", filepath.Join(project, projectskills.Dir, "deploy")); err != nil {
		t.Fatal(err)
	}
	sa := sessionArgs{agent: agent.Default, gatewayURL: defaultGateway, dir: project}
	if _, err := prepareTestSession(sa, sandbox.ModeRun); err == nil || !strings.Contains(err.Error(), ".agents/skills/deploy is a symlink to ../../tools/skills/deploy") {
		t.Fatalf("err = %v, want the link refused", err)
	}
	// Nothing is protected with --skills-writable, so the link is fine.
	sa.skillsWritable = true
	sess, err := prepareTestSession(sa, sandbox.ModeRun)
	if err != nil {
		t.Fatal(err)
	}
	sess.cleanup()
}

func TestSkillsWritableFlag(t *testing.T) {
	for _, name := range []string{"run", "shell"} {
		sa, err := parseOfflineArgs(name, []string{"--skills-writable", "dir"}, nil)
		if err != nil || !sa.skillsWritable || sa.dir != "dir" {
			t.Errorf("%s: %+v, %v", name, sa, err)
		}
	}
	if _, err := parseSessionArgs("check", []string{"--skills-writable"}, nil); err == nil {
		t.Error("check takes --skills-writable")
	}
	if !strings.Contains(usage, "--skills-writable") {
		t.Error("usage lacks --skills-writable")
	}
}
