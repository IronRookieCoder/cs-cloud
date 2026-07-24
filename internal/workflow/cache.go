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
	// Write through a temp file + atomic rename so concurrent readers (the
	// runtime loop and the CLI share this cache) never observe partial JSON.
	tmp, err := os.CreateTemp(c.dir, ".workspaces-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, c.workspacesPath())
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
