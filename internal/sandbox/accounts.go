package sandbox

import "fmt"

const (
	// User is the session user's name inside the agent container. It is
	// fixed, so nothing about the host user is passed in.
	User = "agent"
	// Passwd and Group are where the session's account files are mounted,
	// read-only, over the image's.
	Passwd = "/etc/passwd"
	Group  = "/etc/group"
)

// Accounts returns the passwd and group files for a session run as uid:gid:
// root, and User with HOME at Home. The image only has its own users, so
// without them the host UID has no name, and anything that looks up the
// current user (os.userInfo(), whoami, getpass) fails. A uid or gid of 0 is
// root's entry alone.
func Accounts(uid, gid int) (passwd, group string) {
	passwd = "root:x:0:0:root:/root:/bin/sh\n"
	if uid != 0 {
		passwd += fmt.Sprintf("%s:x:%d:%d:%s:%s:/bin/sh\n", User, uid, gid, User, Home)
	}
	group = "root:x:0:\n"
	if gid != 0 {
		group += fmt.Sprintf("%s:x:%d:\n", User, gid)
	}
	return passwd, group
}
