package core

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/webkaz-labs/sobalink/internal/config"
	"github.com/webkaz-labs/sobalink/internal/resource"
)

func TestResourceOperationRejectsBetweenStageSubstitution(t *testing.T) {
	for _, kind := range []string{"profile", "journal", "lock"} {
		for _, after := range []int{1, 2, 3} {
			t.Run(kind+string(rune('0'+after)), func(t *testing.T) {
				c, _ := resourceFixture(t)
				in := resourceApplyRequest(t, c, 3)
				writes := 0
				c.atomicWrite = func(path string, data []byte) error {
					writes++
					if err := config.AtomicWrite(path, data); err != nil {
						return err
					}
					if writes == after {
						replaceResourceBinding(t, c.dir, kind)
					}
					return nil
				}
				got := resourceCall(t, c, "resource.apply", in).(resource.Operation)
				if got.Outcome.Status != "unknown" || got.EvidenceDurable || !c.resourceFrozen || writes != after {
					t.Fatal("lost binding acknowledged or wrote again", got, writes)
				}
				if kind != "lock" {
					if _, err := os.Stat(resourceStatePath(c.dir)); !os.IsNotExist(err) {
						t.Fatal("evidence appeared in replacement", err)
					}
				}
				raw, _ := json.Marshal(resource.StatusRequest{Target: in.Target, OperationID: in.OperationID})
				if _, err := c.resourceCommand("resource.operation.status", raw); err == nil {
					t.Fatal("lost ownership re-enabled")
				}
			})
		}
	}
}
