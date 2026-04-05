package executor

import (
	billy "github.com/go-git/go-billy/v6"
)

type Result struct {
	FS           billy.Filesystem
	ConfigFile   string
	RuntimeBin   string
	ContextDir   string
	DataDir      string
	BinDir       string
	WorkspaceDir string
	TmpDir       string
	VarLibDir    string
}
