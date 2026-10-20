package sandbox

import "testing"

func TestAccounts(t *testing.T) {
	cases := []struct {
		name          string
		uid, gid      int
		passwd, group string
	}{
		{"macOS user", 501, 20,
			"root:x:0:0:root:/root:/bin/sh\nagent:x:501:20:agent:/home/agent:/bin/sh\n",
			"root:x:0:\nagent:x:20:\n"},
		// The image's node user has 1000; the session user is agent all the same.
		{"Linux user", 1000, 1000,
			"root:x:0:0:root:/root:/bin/sh\nagent:x:1000:1000:agent:/home/agent:/bin/sh\n",
			"root:x:0:\nagent:x:1000:\n"},
		{"root", 0, 0,
			"root:x:0:0:root:/root:/bin/sh\n",
			"root:x:0:\n"},
		{"root group", 501, 0,
			"root:x:0:0:root:/root:/bin/sh\nagent:x:501:0:agent:/home/agent:/bin/sh\n",
			"root:x:0:\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			passwd, group := Accounts(c.uid, c.gid)
			if passwd != c.passwd {
				t.Errorf("passwd = %q, want %q", passwd, c.passwd)
			}
			if group != c.group {
				t.Errorf("group = %q, want %q", group, c.group)
			}
		})
	}
}
