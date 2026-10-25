package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"path"
	"sort"
	"strings"
)

func validFolder(p string) bool {
	if p == "" {
		return true
	}
	if strings.ContainsAny(p, "\\\x00") || path.Clean(p) != p || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func folderParent(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func requestFolder(w http.ResponseWriter, r *http.Request) (*string, bool) {
	if !r.URL.Query().Has("folder") {
		return nil, true
	}
	p := r.URL.Query().Get("folder")
	if r.URL.Query().Get("root") == "" || !validFolder(p) {
		apiError(w, 400, "invalid_folder", "Choose a root and a valid folder path")
		return nil, false
	}
	return &p, true
}

type folder struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	Works       int    `json:"works"`
	CoverWorkID string `json:"cover_work_id"`
}
type folderListing struct {
	RootID   string   `json:"root_id"`
	RootName string   `json:"root_name"`
	Path     string   `json:"path"`
	Works    int      `json:"works"`
	Folders  []folder `json:"folders"`
}

func (a *App) folders(w http.ResponseWriter, r *http.Request, u User) {
	p, root := r.URL.Query().Get("path"), r.URL.Query().Get("root")
	if !validFolder(p) {
		apiError(w, 400, "invalid_folder", "Invalid folder path")
		return
	}
	out := folderListing{RootID: root, Path: p, Folders: []folder{}}
	visible, args := visibleSQL("ra", "r", u)
	e := a.DB.QueryRowContext(r.Context(), "SELECT r.name FROM roots r JOIN (SELECT id AS root_id,status FROM roots) ra ON ra.root_id=r.id WHERE "+visible+" AND r.id=?", append(args, root)...).Scan(&out.RootName)
	if errors.Is(e, sql.ErrNoRows) {
		apiError(w, 404, "not_found", "Root not found")
		return
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot load root")
		return
	}
	visible, args = visibleSQL("al", "r", u)
	query := "SELECT al.parent,json_group_array(DISTINCT al.work_id),min(al.path),al.work_id FROM aliases al JOIN roots r ON r.id=al.root_id WHERE " + visible + " AND al.root_id=?"
	args = append(args, root)
	if p != "" {
		query += " AND (al.parent=? OR (al.parent>=? AND al.parent<?))"
		args = append(args, p, p+"/", p+"0")
	}
	rows, e := a.DB.QueryContext(r.Context(), query+" GROUP BY al.parent", args...)
	if e != nil {
		apiError(w, 503, "database", "Cannot load folders")
		return
	}
	defer rows.Close()
	type child struct {
		folder
		ids       map[string]bool
		coverPath string
	}
	children := map[string]*child{}
	for rows.Next() {
		var parent, raw, coverPath, coverID string
		if e = rows.Scan(&parent, &raw, &coverPath, &coverID); e != nil {
			break
		}
		var ids []string
		if e = json.Unmarshal([]byte(raw), &ids); e != nil {
			break
		}
		if parent == folderParent(p) {
			out.Works = len(ids)
			continue
		}
		relative := parent
		if p != "" {
			relative = strings.TrimPrefix(parent, p+"/")
		}
		name := strings.SplitN(relative, "/", 2)[0]
		c := children[name]
		if c == nil {
			childPath := name
			if p != "" {
				childPath = p + "/" + name
			}
			c = &child{folder: folder{Name: name, Path: childPath}, ids: map[string]bool{}}
			children[name] = c
		}
		for _, id := range ids {
			c.ids[id] = true
		}
		if c.coverPath == "" || coverPath < c.coverPath {
			c.coverPath = coverPath
			c.CoverWorkID = coverID
		}
	}
	if e == nil {
		e = rows.Err()
	}
	if e != nil {
		apiError(w, 503, "database", "Cannot read folders")
		return
	}
	for _, c := range children {
		c.Works = len(c.ids)
		out.Folders = append(out.Folders, c.folder)
	}
	sort.Slice(out.Folders, func(i, j int) bool {
		x, y := out.Folders[i].Name, out.Folders[j].Name
		if naturalKey(x) == naturalKey(y) {
			return x < y
		}
		return naturalKey(x) < naturalKey(y)
	})
	jsonResponse(w, 200, out)
}
