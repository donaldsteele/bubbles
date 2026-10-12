package main

import (
	"context"
	"strconv"
	"time"

	"github.com/donaldsteele/bubbles/pkg/bubblesclient"
)

// ServerInfo is one server found by mDNS browsing, as shown in the picker.
type ServerInfo struct {
	Name          string `json:"name"`
	URL           string `json:"url"`
	OnboardingURL string `json:"onboardingUrl"`
	OnboardPort   string `json:"onboardPort"`
}

// discoverServers browses _bubbles._tcp for a few seconds. Discovery is a
// convenience only: a failure just yields an empty list and the user types
// the address.
func discoverServers(ctx context.Context) []ServerInfo {
	servers, err := bubblesclient.Discover(ctx, 3*time.Second)
	if err != nil {
		return nil
	}
	out := make([]ServerInfo, 0, len(servers))
	for _, s := range servers {
		info := ServerInfo{Name: s.Name, URL: s.URL(), OnboardingURL: s.OnboardingURL()}
		if s.OnboardPort > 0 {
			info.OnboardPort = strconv.Itoa(s.OnboardPort)
		}
		out = append(out, info)
	}
	return out
}
