package appcomponent

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"

	appbackend "github.com/ingot-agent/app-webui"
)

// handleBrowseWorkspace serves the trusted local directory picker used to
// select the absolute Workspace root for a new Session. The browser cannot
// learn host absolute paths from its own file input, so the local app.backend
// (bound to 127.0.0.1 for trusted single-user use) lists directories on
// behalf of the user. Only directory names and their absolute paths are
// disclosed; file contents are never read or served.
func (a *application) handleBrowseWorkspace(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = string(filepath.Separator)
		}
		path = home
	}
	if !filepath.IsAbs(path) {
		writeAPIError(w, http.StatusBadRequest, "workspace_path_required", "workspace browse path must be an absolute directory")
		return
	}
	clean := filepath.Clean(path)
	entries, err := os.ReadDir(clean)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "workspace_browse_failed", "cannot list directory: "+err.Error())
		return
	}
	result := appbackend.WorkspaceBrowse{Path: clean}
	parent := filepath.Dir(clean)
	if parent != clean {
		result.Parent = parent
	} else {
		result.Roots = workspaceRootEntries(clean)
	}
	for _, entry := range entries {
		// Only plain directories are offered so the picker cannot be used to
		// follow symlink cycles or treat files as workspaces.
		if !entry.IsDir() {
			continue
		}
		result.Directories = append(result.Directories, appbackend.WorkspaceBrowseEntry{
			Name: entry.Name(),
			Path: filepath.Join(clean, entry.Name()),
		})
	}
	sort.Slice(result.Directories, func(i, j int) bool {
		return result.Directories[i].Name < result.Directories[j].Name
	})
	writeJSON(w, http.StatusOK, result)
}
