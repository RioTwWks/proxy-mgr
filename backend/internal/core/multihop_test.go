package core

import (
	"encoding/json"
	"strings"
	"testing"

	"rionexgate/internal/config"
	"rionexgate/internal/models"
)

func TestResolveClientEndpointUsesEntryNode(t *testing.T) {
	entry := &models.Node{Address: "entry.ru.example", Port: 443}
	ep := ResolveClientEndpoint("panel.local", 8080, models.User{}, entry)
	if ep.Host != "entry.ru.example" || ep.Port != 443 {
		t.Fatalf("unexpected endpoint: %+v", ep)
	}
}

func TestBuildMultihopDataGeneratesOutbounds(t *testing.T) {
	exit := models.Node{
		ID:       2,
		Name:     "exit-eu",
		Address:  "exit.eu.example",
		Port:     8443,
		Role:     models.NodeRoleExit,
		Protocol: "vless",
		Credentials: `{"uuid":"relay-uuid","flow":"xtls-rprx-vision","security":"reality","public_key":"pk","short_id":"ab12"}`,
	}
	users := []models.User{{Email: "user@example.com", ExitNodeID: &exit.ID}}
	multihop := &config.MultihopConfig{Enabled: true, LocalRole: "entry"}

	data := BuildMultihopData(multihop, users, []models.Node{exit}, func(u models.User) *models.Node {
		return &exit
	})
	if !data.Enabled || len(data.Outbounds) != 1 {
		t.Fatalf("expected one outbound, got %+v", data)
	}
	if data.Outbounds[0].Tag != "exit-exit-eu" {
		t.Fatalf("unexpected tag: %s", data.Outbounds[0].Tag)
	}
	if len(data.Routings) != 1 || data.Routings[0].OutboundTag != "exit-exit-eu" {
		t.Fatalf("unexpected routing: %+v", data.Routings)
	}
}

func TestGenerateMultihopXrayConfig(t *testing.T) {
	exit := models.Node{
		ID:       2,
		Name:     "exit-eu",
		Address:  "exit.eu.example",
		Port:     8443,
		Role:     models.NodeRoleExit,
		Protocol: "vless",
		Credentials: `{"uuid":"relay-uuid","flow":"xtls-rprx-vision","security":"reality","public_key":"pk","short_id":"ab12"}`,
	}
	users := []models.User{{UUID: "u1", Email: "user@example.com"}}
	multihop := BuildMultihopData(
		&config.MultihopConfig{Enabled: true, LocalRole: "entry"},
		users,
		[]models.Node{exit},
		func(models.User) *models.Node { return &exit },
	)

	raw, err := generateXrayConfig(443, "127.0.0.1:10085", users, nil, multihop)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	checks := []string{
		`"tag": "exit-exit-eu"`,
		`"address": "exit.eu.example"`,
		`"protocol": "vless"`,
		`"tag": "exit-exit-eu-chain"`,
		`"proxySettings"`,
		`"routing"`,
		`"outboundTag": "exit-exit-eu-chain"`,
	}
	for _, c := range checks {
		if !strings.Contains(body, c) {
			t.Fatalf("missing %q in config:\n%s", c, body)
		}
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
}

func TestMultihopStealthIncludesUsersInAllInbounds(t *testing.T) {
	exit := models.Node{
		ID:          2,
		Name:        "RelayEU",
		Address:     "exit.eu.example",
		Port:        8443,
		Role:        models.NodeRoleExit,
		Protocol:    "vless",
		Credentials: `{"uuid":"relay-uuid","flow":"xtls-rprx-vision","security":"reality","public_key":"pk","short_id":"ab12"}`,
	}
	userUUID := "e8e5b480-f5c8-4b76-b308-410a338d2c2c"
	users := []models.User{{UUID: userUUID, Email: "test@test.test", ExitNodeID: &exit.ID}}
	stealth := &config.StealthConfig{
		Enabled: true,
		Reality: config.StealthRealityConfig{
			Dest:        "www.microsoft.com:443",
			ServerNames: []string{"www.microsoft.com"},
			PrivateKey:  "SNX6hIY7eBmqDCdiR9HhycMkyuKtRty3PqJnhgAsn3w",
			PublicKey:   "Izo7I-b-XfLZP0jTBHhC3zzHZ02-oX57Z1JwB6fgABM",
			ShortIDs:    []string{"a1b2c3d4"},
		},
		XHTTP: config.StealthXHTTPConfig{Enabled: true, Port: 443, Path: "/api/v1/data", Mode: "stream-one", Tag: "vless-xhttp-reality"},
		Vision: config.StealthVisionConfig{Enabled: true, Port: 8443, Tag: "vless-vision-reality"},
	}
	multihop := BuildMultihopData(
		&config.MultihopConfig{Enabled: true, LocalRole: "entry"},
		users,
		[]models.Node{exit},
		func(models.User) *models.Node { return &exit },
	)

	raw, err := generateXrayConfig(443, "127.0.0.1:10085", users, stealth, multihop)
	if err != nil {
		t.Fatal(err)
	}
	body := string(raw)
	if !strings.Contains(body, `"id": "`+userUUID+`"`) {
		t.Fatalf("user UUID missing from generated config:\n%s", body)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	inbounds, ok := parsed["inbounds"].([]interface{})
	if !ok || len(inbounds) != 2 {
		t.Fatalf("expected 2 stealth inbounds, got %d", len(inbounds))
	}
	for i, inbound := range inbounds {
		ib, ok := inbound.(map[string]interface{})
		if !ok {
			t.Fatalf("inbound %d: unexpected type", i)
		}
		settings, ok := ib["settings"].(map[string]interface{})
		if !ok {
			t.Fatalf("inbound %d: missing settings", i)
		}
		clients, ok := settings["clients"].([]interface{})
		if !ok || len(clients) != 1 {
			t.Fatalf("inbound %d: expected 1 client, got %+v", i, settings["clients"])
		}
		client, ok := clients[0].(map[string]interface{})
		if !ok || client["id"] != userUUID {
			t.Fatalf("inbound %d: unexpected client %+v", i, clients[0])
		}
	}
}

func TestBuildSubscriptionUsesEntryNode(t *testing.T) {
	user := models.User{
		UUID:  "550e8400-e29b-41d4-a716-446655440000",
		Email: "test@example.com",
	}
	entry := &models.Node{Address: "entry.ru.example", Port: 443}
	links := BuildSubscriptionLinks("panel.local", 8080, user, nil, entry, nil)
	if len(links) == 0 {
		t.Fatal("expected links")
	}
	if !strings.Contains(links[0], "entry.ru.example") {
		t.Fatalf("expected entry host in link, got %s", links[0])
	}
	if strings.Contains(links[0], "exit.eu") {
		t.Fatal("exit node must not appear in client links")
	}
}

func TestBuildClientConfigUsesEntryNode(t *testing.T) {
	user := models.User{
		UUID:  "550e8400-e29b-41d4-a716-446655440000",
		Email: "test@example.com",
	}
	entry := &models.Node{Address: "entry.ru.example", Port: 443}
	cfg, err := BuildClientConfig("panel.local", 8080, user, 10808, nil, entry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) == 0 || cfg.Servers[0].Host != "entry.ru.example" {
		t.Fatalf("expected entry host in config, got %+v", cfg.Servers)
	}
}
