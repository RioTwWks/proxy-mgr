package core

import (
	"bytes"
	"embed"
	"net"
	"strings"
	"text/template"

	"rionexgate/internal/config"
	"rionexgate/internal/models"
)

//go:embed templates/*.tmpl
var templateFS embed.FS

type templateData struct {
	ListenPort       int
	APIAddress       string
	APIListenAddress string
	Users            []models.User
	Stealth          *config.StealthConfig
	Multihop         MultihopData
	InboundTags      []string
}

// xrayAPIListenAddress returns the bind address for Xray's stats API in generated config.
// api_address in config.yaml is used by the backend to query stats (often host.docker.internal
// from the bridge network). Xray runs with network_mode: host on Linux and cannot resolve
// host.docker.internal unless it is added to the host's /etc/hosts.
func xrayAPIListenAddress(apiAddress string) string {
	host, port, err := net.SplitHostPort(apiAddress)
	if err != nil {
		return apiAddress
	}
	if host == "host.docker.internal" {
		// Bind on all interfaces so backend (bridge) can reach via host-gateway.
		return "0.0.0.0:" + port
	}
	return apiAddress
}

func renderTemplate(name string, data templateData) ([]byte, error) {
	content, err := templateFS.ReadFile("templates/" + name)
	if err != nil {
		return nil, err
	}
	funcMap := template.FuncMap{
		"jsonStrings":   jsonStringList,
		"stealthSNI":    stealthPrimarySNI,
		"stealthActive": stealthIsActive,
		"stealthFragmentationTLS": stealthFragmentationTLS,
		"stealthFragmentPackets": stealthFragmentPackets,
		"stealthFragmentLength": stealthFragmentLength,
		"stealthFragmentDelay": stealthFragmentDelay,
		"stealthFragmentMaxSplit": stealthFragmentMaxSplit,
	}
	tmpl, err := template.New(name).Funcs(funcMap).Parse(string(content))
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func stealthIsActive(s *config.StealthConfig) bool {
	return s != nil && s.IsActive()
}

func stealthPrimarySNI(s *config.StealthConfig) string {
	if s == nil {
		return ""
	}
	return s.PrimarySNI()
}


func stealthFragmentationTLS(s *config.StealthConfig) bool { return s != nil && s.FragmentationApplicable() }
func stealthFragmentPackets(s *config.StealthConfig) string {
	if s == nil { return "tlshello" }
	return s.Fragmentation.PacketsValue()
}
func stealthFragmentLength(s *config.StealthConfig) string {
	if s == nil { return "50-100" }
	return s.Fragmentation.LengthOrDefault()
}
func stealthFragmentDelay(s *config.StealthConfig) string {
	if s == nil { return "10-20" }
	return s.Fragmentation.DelayOrDefault()
}
func stealthFragmentMaxSplit(s *config.StealthConfig) string {
	if s == nil { return "2-4" }
	return s.Fragmentation.MaxSplitOrDefault()
}

func jsonStringList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = `"` + item + `"`
	}
	return strings.Join(quoted, ", ")
}

func generateXrayConfig(listenPort int, apiAddress string, users []models.User, stealth *config.StealthConfig, multihop MultihopData) ([]byte, error) {
	return renderTemplate("xray.json.tmpl", templateData{
		ListenPort:       listenPort,
		APIAddress:       apiAddress,
		APIListenAddress: xrayAPIListenAddress(apiAddress),
		Users:            users,
		Stealth:          stealth,
		Multihop:         multihop,
		InboundTags:      CollectInboundTags(stealth, "vless-in"),
	})
}

func generateSingboxConfig(listenPort int, apiAddress string, users []models.User, stealth *config.StealthConfig, multihop MultihopData) ([]byte, error) {
	return renderTemplate("singbox.json.tmpl", templateData{
		ListenPort: listenPort,
		APIAddress: apiAddress,
		Users:      users,
		Stealth:    stealth,
		Multihop:   multihop,
	})
}
