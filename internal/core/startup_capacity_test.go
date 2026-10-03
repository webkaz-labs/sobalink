package core

import (
	"encoding/json"
	"github.com/webkaz-labs/sobalink/internal/capacity"
	"os"
	"path/filepath"
	"testing"
)

func TestStartupCapacityRejectsUnreadablePrivateStateOnPreview(t *testing.T) {
	c, spec := startupFixture(t)
	saveStartupFixture(t, c, spec)
	path := filepath.Join(c.dir, "startup.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, make([]byte, 2048)...)
	for i := len(data) - 2048; i < len(data); i++ {
		data[i] = ' '
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	policy := c.capacityPolicy()
	policy.Resources["profileBytes"] = capacity.Limited(1024)
	if _, err := command(c, randomID(), "policy.preview", map[string]any{"policy": policy}); networkErrorCode(err) != "policy_in_use" {
		t.Fatal("smaller capacity preview accepted retained private state", err)
	}
	usage := c.capacityUsage()
	if usage["startupBytes"] != int64(len(data)) || usage["profileBytes"] < usage["startupBytes"] {
		t.Fatal("private bytes not accounted")
	}
	public, _ := json.Marshal(usage)
	if len(public) == 0 {
		t.Fatal("missing public numeric usage")
	}
}
