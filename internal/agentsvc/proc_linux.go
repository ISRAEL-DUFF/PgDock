package agentsvc

import (
	"os/exec"
	"syscall"
)

// dieWithAgent makes the child get SIGKILL if the agent dies, so a crashed
// agent leaves no pg_dump or pg_restore holding locks or connections.
func dieWithAgent(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL}
}
