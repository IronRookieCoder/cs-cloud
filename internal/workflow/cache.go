package workflow

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

type Cache struct {
	dir string
}

func NewCache(dir string) *Cache {
	return &Cache{dir: dir}
}

func (c *Cache) ensureDir() error {
	return os.MkdirAll(c.dir, 0o755)
}

func (c *Cache) workspacesPath() string {
	return filepath.Join(c.dir, "workspaces.json")
}

func (c *Cache) WriteWorkspaces(wss []Workspace) error {
	if err := c.ensureDir(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(wss, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.workspacesPath(), b, 0o644)
}

func (c *Cache) ReadWorkspaces() ([]Workspace, error) {
	b, err := os.ReadFile(c.workspacesPath())
	if err != nil {
		if os.IsNotExist(err) {
			return []Workspace{}, nil
		}
		return nil, err
	}
	var wss []Workspace
	if err := json.Unmarshal(b, &wss); err != nil {
		return nil, fmt.Errorf("unmarshal workspaces: %w", err)
	}
	return wss, nil
}
