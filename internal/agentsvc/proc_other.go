//go:build !linux

package agentsvc

import "os/exec"

func dieWithAgent(*exec.Cmd) {}
