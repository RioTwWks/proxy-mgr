package core

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"rionexgate/internal/models"
)

func TestBuildVLESSLink(t *testing.T) {
	user := models.User{
		UUID:  "550e8400-e29b-41d4-a716-446655440000",
		Email: "test@example.com",
	}
	link := GetClientLink("example.com", 443, user, "vless", nil)
	if !strings.HasPrefix(link, "vless://") {
		t.Fatalf("expected vless prefix, got %s", link)
	}
	if !strings.Contains(link, user.UUID) {
		t.Fatalf("expected uuid in link")
	}
	if !strings.Contains(link, "example.com:443") {
		t.Fatalf("expected host:port in link")
	}
}

func TestBuildVMessLink(t *testing.T) {
	user := models.User{
		UUID:  "550e8400-e29b-41d4-a716-446655440000",
		Email: "test@example.com",
	}
	link := GetClientLink("example.com", 443, user, "vmess", nil)
	if !strings.HasPrefix(link, "vmess://") {
		t.Fatalf("expected vmess prefix, got %s", link)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(link, "vmess://"))
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["id"] != user.UUID || payload["add"] != "example.com" {
		t.Fatalf("unexpected vmess payload: %+v", payload)
	}
}

func TestBuildTrojanLink(t *testing.T) {
	user := models.User{
		UUID:  "550e8400-e29b-41d4-a716-446655440000",
		Email: "test@example.com",
	}
	link := GetClientLink("example.com", 443, user, "trojan", nil)
	if !strings.HasPrefix(link, "trojan://") {
		t.Fatalf("expected trojan prefix, got %s", link)
	}
	if !strings.Contains(link, user.UUID+"@example.com:443") {
		t.Fatalf("expected uuid@host:port in link, got %s", link)
	}
}

func TestGenerateXrayConfig(t *testing.T) {
	users := []models.User{
		{UUID: "uuid-1", Email: "a@example.com"},
		{UUID: "uuid-2", Email: "b@example.com"},
	}
	data, err := generateXrayConfig(443, "127.0.0.1:10085", users, nil, MultihopData{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "uuid-1") || !strings.Contains(body, "a@example.com") {
		t.Fatalf("missing user in config: %s", body)
	}
	if !strings.Contains(body, `"listen": "127.0.0.1:10085"`) {
		t.Fatalf("expected api listen address in config: %s", body)
	}
}

func TestGenerateXrayConfigDockerInternalListen(t *testing.T) {
	data, err := generateXrayConfig(443, "host.docker.internal:10085", nil, nil, MultihopData{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, `"listen": "0.0.0.0:10085"`) {
		t.Fatalf("expected host-network bind address, got: %s", body)
	}
}

func TestGenerateSingboxConfig(t *testing.T) {
	users := []models.User{
		{UUID: "uuid-1", Email: "a@example.com"},
	}
	data, err := generateSingboxConfig(443, "127.0.0.1:9090", users, nil, MultihopData{})
	if err != nil {
		t.Fatal(err)
	}
	body := string(data)
	if !strings.Contains(body, "uuid-1") {
		t.Fatalf("missing user in sing-box config: %s", body)
	}
}
