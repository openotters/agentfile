package executor

import (
	"context"

	billy "github.com/go-git/go-billy/v6"
	"oras.land/oras-go/v2/content/memory"
)

type Result struct {
	FS           billy.Filesystem
	ContextDir   string
	DataDir      string
	BinDir       string
	WorkspaceDir string
	TmpDir       string
	VarLibDir    string
}

type Executor interface {
	Execute(ctx context.Context, store *memory.Store, ref string) (*Result, error)
}
