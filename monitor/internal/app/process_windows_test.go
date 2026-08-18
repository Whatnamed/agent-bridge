//go:build windows

package app

import "testing"

func TestSupervisedServerCommandUsesDetachedCreationFlags(t *testing.T) {
	command := newSupervisedServerCommand("test-server.exe", defaultConfig())
	if command.SysProcAttr == nil {
		t.Fatal("supervised server command has no Windows process attributes")
	}

	const (
		detachedProcess       = 0x00000008
		createNewProcessGroup = 0x00000200
	)
	flags := command.SysProcAttr.CreationFlags
	if flags&detachedProcess == 0 {
		t.Fatalf("supervised server command is not detached: flags=%#x", flags)
	}
	if flags&createNewProcessGroup == 0 {
		t.Fatalf("supervised server command is not isolated in a new process group: flags=%#x", flags)
	}
}
