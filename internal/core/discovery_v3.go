package core

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"
)

const discoveryV3Path = "/.well-known/sobalink/services/v3"

type servicePage struct {
	Version       int             `json:"version"`
	Services      []RemoteService `json:"services"`
	Revision      string          `json:"revision"`
	NextCursor    string          `json:"nextCursor,omitempty"`
	receivedBytes int64
}

func (c *Core) serveServiceDiscovery(w http.ResponseWriter, r *http.Request, peerID string) {
	services := c.permittedServices(peerID)
	sort.Slice(services, func(i, j int) bool { return services[i].ID < services[j].ID })
	if r.URL.Path != "/.well-known/sobalink/services/v2" {
		data, _ := json.Marshal(services)
		hash := sha256.Sum256(data)
		revision := hex.EncodeToString(hash[:])
		if expected := r.URL.Query().Get("revision"); expected != "" && expected != revision {
			reply(w, 409, map[string]string{"code": "discovery_changed", "error": "services changed; restart discovery"})
			return
		}
		after := r.URL.Query().Get("after")
		start := 0
		if after != "" {
			start = sort.Search(len(services), func(i int) bool { return services[i].ID >= after })
			if start == len(services) || services[start].ID != after {
				peerFailure(w, 400)
				return
			}
			start++
		}
		page := servicePage{Version: 3, Services: []RemoteService{}, Revision: revision}
		budget := min(c.limit("resources", "discoveryBytes"), c.limit("resources", "pageBytes"))
		if encoded, _ := json.Marshal(page); int64(len(encoded))+1 > budget {
			reply(w, 413, map[string]string{"code": "discovery_capacity", "error": "discovery framing exceeds the configured page budget"})
			return
		}
		for i := start; i < len(services) && int64(len(page.Services)) < c.limit("resources", "pageEntries"); i++ {
			page.Services = append(page.Services, services[i])
			page.NextCursor = ""
			if i+1 < len(services) {
				page.NextCursor = services[i].ID
			}
			encoded, _ := json.Marshal(page)
			if int64(len(encoded))+1 > budget {
				page.Services = page.Services[:len(page.Services)-1]
				if len(page.Services) == 0 {
					reply(w, 413, map[string]string{"code": "discovery_capacity", "error": "one service exceeds the configured discovery page budget"})
					return
				}
				page.NextCursor = page.Services[len(page.Services)-1].ID
				break
			}
		}
		reply(w, 200, page)
		return
	}
	// v2 has no permanent-grant representation. Do not substitute a fabricated
	// expiry or silently truncate a newer scope for an old client.
	if len(services) > 64 {
		reply(w, 409, map[string]string{"code": "discovery_upgrade_required", "error": "use discovery version 3 for this service list"})
		return
	}
	for i := range services {
		if services[i].Lifetime == "until-revoked" || services[i].ExpiresAt.After(time.Now().Add(24*time.Hour)) {
			reply(w, 409, map[string]string{"code": "discovery_upgrade_required", "error": "use discovery version 3 for this lifetime"})
			return
		}
		services[i].Lifetime = ""
	}
	reply(w, 200, map[string]any{"version": 2, "services": services})
}

func (c *Core) queryServicePages(ctx context.Context, id string) ([]RemoteService, error) {
	all := []RemoteService{}
	revision, cursor := "", ""
	seen := map[string]bool{}
	var used int64
	for {
		path := discoveryV3Path
		if cursor != "" {
			path += "?after=" + url.QueryEscape(cursor) + "&revision=" + url.QueryEscape(revision)
		}
		var page servicePage
		if err := c.peerJSON(ctx, id, "GET", path, nil, &page); err != nil {
			return nil, err
		}
		_, revisionErr := hex.DecodeString(page.Revision)
		if page.Version != 3 || page.Services == nil || len(page.Revision) != 64 || revisionErr != nil || revision != "" && page.Revision != revision {
			return nil, errors.New("invalid service discovery page")
		}
		used += page.receivedBytes
		if used > c.limit("resources", "discoveryBytes") {
			return nil, &localCommandError{"discovery_capacity", "service list exceeds the configured discovery response budget; raise discoveryBytes"}
		}
		for _, service := range page.Services {
			if service.Lifetime != "finite" && service.Lifetime != "until-revoked" || seen[service.ID] || validateRemote(service) != nil {
				return nil, errors.New("invalid or unknown service lifetime metadata")
			}
			seen[service.ID] = true
			all = append(all, service)
		}
		if page.NextCursor == "" {
			return all, nil
		}
		if len(page.Services) == 0 || page.NextCursor != page.Services[len(page.Services)-1].ID || page.NextCursor <= cursor {
			return nil, errors.New("invalid service discovery cursor")
		}
		cursor, revision = page.NextCursor, page.Revision
	}
}
