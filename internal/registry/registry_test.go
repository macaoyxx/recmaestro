package registry

import (
	"net"
	"testing"
)

func TestLocalIP(t *testing.T) {
	ip := LocalIP()
	if net.ParseIP(ip) == nil {
		t.Errorf("LocalIP() = %q, 不是合法 IP", ip)
	}
}

func TestNewService(t *testing.T) {
	svc := NewService("recmaestro-worker", "172.21.0.6", 8080)
	want := "recmaestro-worker-172.21.0.6-8080"
	if svc.ID != want {
		t.Errorf("ID = %q, want %q", svc.ID, want)
	}
	if svc.Addr() != "172.21.0.6:8080" {
		t.Errorf("Addr() = %q, want 172.21.0.6:8080", svc.Addr())
	}
}
