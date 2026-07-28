package localserver

import "errors"

var (
	errWorkspaceIsRoot = errors.New("path is the workspace root")
	errPathEscapes     = errors.New("path escapes workspace root")
)
