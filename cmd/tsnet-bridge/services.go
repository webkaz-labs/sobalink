package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/webkaz-labs/tsnet-bridge/internal/app"
	"github.com/webkaz-labs/tsnet-bridge/internal/config"
)

// Service discovery confirms an authenticated sharing observation, never the
// application behind it. Only fresh, unexpired observations are selectable.
func selectableService(service app.DiscoveredService, now time.Time) bool {
	_, err := templateFor(service.Purpose)
	return err == nil && config.ValidPeerID(service.PeerID) && config.ValidHost(service.PeerHost) &&
		validDiscoveredServiceID(service.ID) && (service.Network == "tcp" || service.Network == "udp") &&
		service.Port > 0 && service.Port <= 65535 && service.ExpiresAt.After(now) &&
		!service.CheckedAt.IsZero() && now.Sub(service.CheckedAt) <= app.DiscoveryTTL &&
		!service.CheckedAt.After(now.Add(5*time.Second)) && service.Application == "unverified"
}

func validDiscoveredServiceID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func currentServices(ctx context.Context, dir, peerID string) (app.ServiceCatalog, error) {
	var catalog app.ServiceCatalog
	command := "services"
	if peerID != "" {
		command += ":" + peerID
	}
	if err := call(ctx, dir, command, &catalog); err != nil {
		return app.ServiceCatalog{}, err
	}
	now := time.Now()
	eligible := make([]app.DiscoveredService, 0, len(catalog.Services))
	for _, service := range catalog.Services {
		if selectableService(service, now) && (peerID == "" || service.PeerID == peerID) {
			eligible = append(eligible, service)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		a, b := eligible[i], eligible[j]
		if a.PeerHost != b.PeerHost {
			return a.PeerHost < b.PeerHost
		}
		if a.Purpose != b.Purpose {
			return a.Purpose < b.Purpose
		}
		if a.Network != b.Network {
			return a.Network < b.Network
		}
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		return a.ID < b.ID
	})
	catalog.Services = eligible
	return catalog, nil
}

// A nil selection means that the user explicitly chose the manual path.
func chooseServicePrompt(ctx context.Context, dir string, p *prompts) (*app.DiscoveredService, error) {
	for {
		fmt.Fprintln(p.out, "Checking services shared with this node...")
		catalog, err := currentServices(ctx, dir, "")
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			fmt.Fprintln(p.out, "Shared-service discovery is unavailable. Refresh to retry, or choose manual configuration for a known service.")
			fmt.Fprintln(p.out, err)
		} else {
			printServiceCatalog(p.out, catalog)
		}
		for {
			answer, err := p.ask("Choose service number, r refresh, m manual, or q cancel: ")
			if err != nil {
				return nil, err
			}
			switch strings.ToLower(answer) {
			case "r", "refresh", "更新":
			case "m", "manual", "手動", "詳細":
				fmt.Fprintln(p.out, "Manual configuration: the remote service has not been confirmed by discovery.")
				return nil, nil
			default:
				n, parseErr := strconv.Atoi(answer)
				if parseErr != nil || n < 1 || n > len(catalog.Services) {
					fmt.Fprintln(p.out, "Choose a displayed service number, r to refresh, m for manual configuration, or q to cancel.")
					continue
				}
				selected := catalog.Services[n-1]
				if !selectableService(selected, time.Now()) {
					fmt.Fprintln(p.out, "This sharing observation is stale or expired. Refreshing the service list; choose again.")
					break
				}
				return &selected, nil
			}
			break
		}
	}
}

func printServiceCatalog(out io.Writer, catalog app.ServiceCatalog) {
	fmt.Fprintln(out, "Services shared with this node (authenticated recent responses):")
	for i, service := range catalog.Services {
		fmt.Fprintf(out, "%d. %s | %s | %s:%d\n", i+1, service.PeerHost, service.Purpose, service.Network, service.Port)
		fmt.Fprintf(out, "   Checked: %s | Sharing expires: %s\n", service.CheckedAt.Format(time.RFC3339), service.ExpiresAt.Format(time.RFC3339))
	}
	if len(catalog.Services) == 0 {
		fmt.Fprintln(out, "No currently confirmed shares for this node. The other peer may need to start sharing and allow this node; refresh after checking.")
	}
	unavailable, unsupported := 0, 0
	for _, peer := range catalog.Peers {
		switch peer.State {
		case "unavailable":
			unavailable++
		case "unsupported":
			unsupported++
		}
	}
	if unavailable > 0 || unsupported > 0 {
		fmt.Fprintf(out, "Discovery unconfirmed: %d peers; unsupported: %d peers. This does not establish that their services are stopped.\n", unavailable, unsupported)
	}
	if catalog.Truncated {
		fmt.Fprintln(out, "The discovery scan was limited. Refresh, or use manual configuration for a known service.")
	}
	fmt.Fprintln(out, "Application behavior is unverified. Ordinary Tailscale services and older bridges remain available through manual configuration.")
}

func applyDiscoveredService(r *config.Rule, service app.DiscoveredService) {
	r.PeerID, r.TargetHost = service.PeerID, service.PeerHost
	r.Purpose, r.Network, r.TargetPort = service.Purpose, service.Network, service.Port
}

// A live task may renew the same share lease. Accept a later expiry only when
// the grant token and every endpoint field remain unchanged. A shorter lease
// changes what the user reviewed and requires selecting the share again.
func sameDiscoveredService(a, b app.DiscoveredService) bool {
	return a.ID == b.ID && a.PeerID == b.PeerID && a.PeerHost == b.PeerHost &&
		a.Purpose == b.Purpose && a.Network == b.Network && a.Port == b.Port && !b.ExpiresAt.Before(a.ExpiresAt)
}

func revalidateService(ctx context.Context, dir string, selected *app.DiscoveredService) error {
	if selected == nil {
		return nil
	}
	catalog, err := currentServices(ctx, dir, selected.PeerID)
	if err != nil {
		return errors.New("selected share could not be rechecked; refresh and select it again before saving or starting")
	}
	for _, service := range catalog.Services {
		if sameDiscoveredService(*selected, service) {
			*selected = service
			return nil
		}
	}
	return errors.New("selected share changed, expired or is no longer available to this node; refresh and select again")
}

// Remote metadata in a discovered selection is immutable. The user either
// chooses another fresh service or deliberately enters the manual wizard.
func editDiscoveredRule(ctx context.Context, dir, answer string, r *config.Rule, selected **app.DiscoveredService, p *prompts) (bool, error) {
	switch strings.ToLower(answer) {
	case "s", "service", "services", "back", "戻る", "サービス", "p", "peer", "peers", "相手":
		next, err := chooseServicePrompt(ctx, dir, p)
		if err != nil {
			return true, err
		}
		if next != nil {
			applyDiscoveredService(r, *next)
			*selected = next
			return true, ensureSelectedLocalPort(ctx, r, p)
		}
		// The picker returned nil only after an explicit manual choice.
	case "m", "manual", "手動", "詳細":
		fmt.Fprintln(p.out, "Manual configuration: the remote service has not been confirmed by discovery.")
	case "u", "purpose", "用途":
		fmt.Fprintln(p.out, "To change the remote service, choose s to select a share or m for manual configuration.")
		return true, nil
	case "e", "edit", "編集":
		var err error
		r.ListenPort, err = promptPort(p, fmt.Sprintf("Listen port [%d]: ", r.ListenPort), r.ListenPort, true, func(port int) error {
			return checkPort(ctx, r.Network, port)
		})
		return true, err
	default:
		return false, nil
	}
	peers, err := currentPeers(ctx, dir)
	if err != nil {
		return true, err
	}
	if len(peers) == 0 {
		return true, errors.New("no eligible current peers; check tailnet sign-in and permissions")
	}
	refs, err := choosePeersPrompt(peers, "", false, p)
	if err != nil {
		return true, err
	}
	purpose, refs, err := choosePurposePrompt(peers, refs, false, r.Purpose, p)
	if err != nil {
		return true, err
	}
	r.PeerID, r.TargetHost, r.Purpose = refs[0].ID, refs[0].Host, purpose
	r.TargetPort, err = promptPort(p, fmt.Sprintf("Service port [%d]: ", r.TargetPort), r.TargetPort, false, nil)
	if err != nil {
		return true, err
	}
	*selected = nil
	return true, ensureSelectedLocalPort(ctx, r, p)
}

func ensureSelectedLocalPort(ctx context.Context, r *config.Rule, p *prompts) error {
	if r.ListenPort >= 1024 && checkPort(ctx, r.Network, r.ListenPort) == nil {
		return nil
	}
	fmt.Fprintf(p.out, "Local port %d is unavailable. Choose another port, or q to cancel.\n", r.ListenPort)
	var err error
	r.ListenPort, err = promptPort(p, fmt.Sprintf("Listen port [%d]: ", suggestPort(r.TargetPort)), suggestPort(r.TargetPort), true, func(port int) error {
		return checkPort(ctx, r.Network, port)
	})
	return err
}
