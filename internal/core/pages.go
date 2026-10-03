package core

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
)

type localPage struct {
	Version    int              `json:"version"`
	Items      []map[string]any `json:"items"`
	Revision   string           `json:"revision"`
	NextCursor string           `json:"nextCursor,omitempty"`
	Total      int              `json:"total"`
}

func (c *Core) pageRows(rows []map[string]any, after, expected string) (localPage, error) {
	rows = append([]map[string]any(nil), rows...)
	sort.Slice(rows, func(i, j int) bool { return rows[i]["id"].(string) < rows[j]["id"].(string) })
	encoded, err := json.Marshal(rows)
	if err != nil {
		return localPage{}, err
	}
	hash := sha256.Sum256(encoded)
	revision := hex.EncodeToString(hash[:])
	if expected != "" && expected != revision {
		return localPage{}, &localCommandError{"page_changed", "list changed; reload its first page"}
	}
	start := 0
	if after != "" {
		if expected == "" {
			return localPage{}, errors.New("continuation requires the reviewed page revision")
		}
		start = sort.Search(len(rows), func(i int) bool { return rows[i]["id"].(string) >= after })
		if start == len(rows) || rows[start]["id"] != after {
			return localPage{}, errors.New("unknown page cursor")
		}
		start++
	}
	page := localPage{Version: 1, Items: []map[string]any{}, Revision: revision, Total: len(rows)}
	if encoded, _ := json.Marshal(page); int64(len(encoded))+1 > c.limit("resources", "pageBytes") {
		return localPage{}, &localCommandError{"page_capacity", "list framing exceeds pageBytes; raise the finite page budget"}
	}
	for i := start; i < len(rows) && int64(len(page.Items)) < c.limit("resources", "pageEntries"); i++ {
		page.Items = append(page.Items, rows[i])
		page.NextCursor = ""
		if i+1 < len(rows) {
			page.NextCursor = rows[i]["id"].(string)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			return localPage{}, err
		}
		if int64(len(encoded))+1 > c.limit("resources", "pageBytes") {
			page.Items = page.Items[:len(page.Items)-1]
			if len(page.Items) == 0 {
				return localPage{}, &localCommandError{"page_capacity", "one list item exceeds pageBytes; raise the finite page budget"}
			}
			page.NextCursor = page.Items[len(page.Items)-1]["id"].(string)
			break
		}
	}
	return page, nil
}

func (c *Core) listServices(raw json.RawMessage) (any, error) {
	var in struct {
		Direction string `json:"direction"`
		Cursor    string `json:"cursor"`
		Revision  string `json:"revision"`
	}
	if err := decodePayload(raw, &in); err != nil {
		return nil, err
	}
	if in.Direction != "" && in.Direction != "share" && in.Direction != "forward" {
		return nil, errors.New("direction must be share or forward")
	}
	rows := []map[string]any{}
	for _, row := range c.serviceViews() {
		if in.Direction == "" || row["direction"] == in.Direction {
			rows = append(rows, row)
		}
	}
	return c.pageRows(rows, in.Cursor, in.Revision)
}
