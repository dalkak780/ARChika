package app

import (
	"context"
	"os"
	"strings"
)

func sameObjectKey(a, b string) bool {
	return a != "" && b != "" && (a == b || strings.HasPrefix(a, b+":born=") || strings.HasPrefix(b, a+":born="))
}
func (a *App) hasOtherPhysicalAlias(ctx context.Context, work, root, path, key string) (bool, error) {
	rows, e := a.DB.QueryContext(ctx, "SELECT r.path,al.path FROM aliases al JOIN roots r ON r.id=al.root_id WHERE al.work_id=? AND al.status='online' AND r.status='online' AND NOT(al.root_id=? AND al.path=?) ORDER BY al.root_id,al.path LIMIT 16", work, root, path)
	if e != nil {
		return false, e
	}
	type location struct{ base, path string }
	locations := []location{}
	for rows.Next() {
		var v location
		if e = rows.Scan(&v.base, &v.path); e != nil {
			rows.Close()
			return false, e
		}
		locations = append(locations, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return false, e
	}
	for _, v := range locations {
		r, e := os.OpenRoot(v.base)
		if e != nil {
			continue
		}
		st, e := r.Stat(v.path)
		same := e == nil && sameObjectKey(physicalKey(st, v.base, v.path), key)
		r.Close()
		if same {
			return true, nil
		}
	}
	return false, nil
}
