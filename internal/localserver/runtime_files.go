package localserver

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type fileEntry struct {
	Name     string    `json:"name" example:"main.go"`
	Type     string    `json:"type" example:"file"`
	Size     int64     `json:"size" example:"1024"`
	Modified time.Time `json:"modified"`
}

type fileListData struct {
	Path    string      `json:"path" example:"/home/user/project"`
	Entries []fileEntry `json:"entries"`
}

// @Summary      List files and directories
// @Description  List files and subdirectories under a given path. Paths are sandboxed to the workspace directory (set via X-Workspace-Directory header).
// @Tags         Runtime
// @Produce      json
// @Param        path       query  string  false  "File or directory path (relative to workspace or absolute if allowed)"  default(".")
// @Param        recursive  query  string  false  "Recursive listing"  Enums(true, false)  default(false)
// @Param        limit      query  int     false  "Max entries to return"  minimum(1)  default(1000)
// @Success      200  {object}  envelope{data=fileListData}
// @Failure      400  {object}  envelope
// @Failure      404  {object}  envelope
// @Router       /runtime/files [get]
func (s *Server) handleFileList(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("roots") == "true" {
		writeOK(w, fileListData{Path: "", Entries: listRoots()})
		return
	}

	dirPath := decodeQueryParam(r.URL.Query().Get("path"))
	if dirPath == "" {
		dirPath = "."
	}

	recursive := r.URL.Query().Get("recursive") == "true"
	limit := 1000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	absPath, _, err := s.resolvePath(r, dirPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("path not found: %s", absPath))
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if !info.IsDir() {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("not a directory: %s", absPath))
		return
	}

	var entries []fileEntry
	if recursive {
		entries, err = walkDir(absPath, absPath, limit)
	} else {
		entries, err = readDir(absPath, limit)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	writeOK(w, fileListData{
		Path:    absPath,
		Entries: entries,
	})
}

func listRoots() []fileEntry {
	if runtime.GOOS == "windows" {
		var entries []fileEntry
		for c := 'A'; c <= 'Z'; c++ {
			root := string(c) + `:\`
			info, err := os.Stat(root)
			if err != nil || !info.IsDir() {
				continue
			}
			entries = append(entries, fileEntry{
				Name:     string(c) + ":/",
				Type:     "directory",
				Modified: info.ModTime().UTC(),
			})
		}
		return entries
	}
	info, _ := os.Stat("/")
	modified := time.Time{}
	if info != nil {
		modified = info.ModTime().UTC()
	}
	return []fileEntry{{Name: "/", Type: "directory", Modified: modified}}
}

func readDir(dir string, limit int) ([]fileEntry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].IsDir() != entries[j].IsDir() {
			return entries[i].IsDir()
		}
		return entries[i].Name() < entries[j].Name()
	})

	result := make([]fileEntry, 0, min(len(entries), limit))
	for _, e := range entries {
		if len(result) >= limit {
			break
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		typ := "file"
		if e.IsDir() {
			typ = "directory"
		}
		result = append(result, fileEntry{
			Name:     e.Name(),
			Type:     typ,
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
	}
	return result, nil
}

func walkDir(root, current string, limit int) ([]fileEntry, error) {
	var result []fileEntry
	err := filepath.WalkDir(current, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(result) >= limit {
			return fs.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		typ := "file"
		if d.IsDir() {
			typ = "directory"
		}
		result = append(result, fileEntry{
			Name:     rel,
			Type:     typ,
			Size:     info.Size(),
			Modified: info.ModTime().UTC(),
		})
		return nil
	})
	return result, err
}

type fileContentData struct {
	Path       string `json:"path" example:"/home/user/project/main.go"`
	Content    string `json:"content"`
	Lines      int    `json:"lines" example:"50"`
	Offset     int    `json:"offset" example:"1"`
	TotalLines int    `json:"total_lines" example:"200"`
}

type fileMetaData struct {
	Path     string    `json:"path" example:"/home/user/project/main.go"`
	Size     int64     `json:"size" example:"1024"`
	Modified time.Time `json:"modified"`
	Type     string    `json:"type" example:"file"`
}

// @Summary      Get file metadata
// @Description  Read metadata for a file or directory. Path is sandboxed to the workspace directory.
// @Tags         Runtime
// @Produce      json
// @Param        path  query  string  true  "File path (relative to workspace or absolute if allowed)"
// @Success      200  {object}  envelope{data=fileMetaData}
// @Failure      400  {object}  envelope
// @Failure      404  {object}  envelope
// @Router       /runtime/files/meta [get]
func (s *Server) handleFileMeta(w http.ResponseWriter, r *http.Request) {
	filePath := decodeQueryParam(r.URL.Query().Get("path"))
	if filePath == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "path is required")
		return
	}

	absPath, _, err := s.resolvePath(r, filePath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("path not found: %s", absPath))
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	typ := "file"
	if info.IsDir() {
		typ = "directory"
	}

	writeOK(w, fileMetaData{
		Path:     absPath,
		Size:     info.Size(),
		Modified: info.ModTime().UTC(),
		Type:     typ,
	})
}

// @Summary      Read file content
// @Description  Read file content with line-based slicing. Path is sandboxed to the workspace directory.
// @Tags         Runtime
// @Produce      json
// @Param        path    query  string  true  "File path (relative to workspace or absolute if allowed)"
// @Param        offset  query  int     false  "Starting line number (1-indexed)"  minimum(1)  default(1)
// @Param        limit   query  int     false  "Max lines to return"  minimum(1)  default(2000)
// @Success      200  {object}  envelope{data=fileContentData}
// @Failure      400  {object}  envelope
// @Failure      404  {object}  envelope
// @Router       /runtime/files/content [get]
func (s *Server) handleFileContent(w http.ResponseWriter, r *http.Request) {
	filePath := decodeQueryParam(r.URL.Query().Get("path"))
	if filePath == "" {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", "path is required")
		return
	}

	offset := 1
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			offset = n
		}
	}

	limit := 2000
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}

	absPath, _, err := s.resolvePath(r, filePath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", err.Error())
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		if os.IsNotExist(err) {
			writeErr(w, http.StatusNotFound, "NOT_FOUND", fmt.Sprintf("file not found: %s", absPath))
			return
		}
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	if info.IsDir() {
		writeErr(w, http.StatusBadRequest, "BAD_REQUEST", fmt.Sprintf("not a file: %s", absPath))
		return
	}

	if info.Size() > 0 {
		f, err := os.Open(absPath)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
			return
		}
		probeSize := int64(512)
		if info.Size() < probeSize {
			probeSize = info.Size()
		}
		buf := make([]byte, probeSize)
		n, _ := f.Read(buf)
		f.Close()
		if bytes.IndexByte(buf[:n], 0) >= 0 {
			writeErr(w, http.StatusUnprocessableEntity, "BINARY_FILE", fmt.Sprintf("file is not a text file: %s", absPath))
			return
		}
	}

	f, err := os.Open(absPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}
	defer f.Close()

	var allLines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		allLines = append(allLines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, "INTERNAL", err.Error())
		return
	}

	totalLines := len(allLines)
	start := offset - 1
	if start > totalLines {
		start = totalLines
	}
	end := start + limit
	if end > totalLines {
		end = totalLines
	}

	selected := allLines[start:end]
	content := strings.Join(selected, "\n")
	if len(selected) > 0 {
		content += "\n"
	}

	writeOK(w, fileContentData{
		Path:       absPath,
		Content:    content,
		Lines:      len(selected),
		Offset:     offset,
		TotalLines: totalLines,
	})
}
