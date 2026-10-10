package core

import (
	"bytes"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/directlan"
	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// This structural regression guards the actual store mutex, not a surrogate
// test lock or scheduling hook. It does not reproduce a concurrent interleaving.
func TestResourceGrantRelationshipClockSampleUnderStoreMutex(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "resource_grant_commands.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fn *ast.FuncDecl
	for _, decl := range f.Decls {
		if candidate, ok := decl.(*ast.FuncDecl); ok && candidate.Name.Name == "resourceGrantRelationship" {
			fn = candidate
		}
	}
	if fn == nil || fn.Body == nil || len(fn.Type.Params.List) != 1 {
		t.Fatal("relationship lookup must own its clock sample")
	}
	param := fn.Type.Params.List[0]
	if len(param.Names) != 1 || param.Names[0].Name != "peer" || !relationshipClockSelector(param.Type, "string") {
		t.Fatal("relationship lookup accepts a caller clock or callback")
	}
	storeIndex, lockIndex, projectionIndex := -1, -1, -1
	for i, stmt := range fn.Body.List {
		switch stmt := stmt.(type) {
		case *ast.AssignStmt:
			if len(stmt.Lhs) == 1 && relationshipClockSelector(stmt.Lhs[0], "s") && len(stmt.Rhs) == 1 && stmt.Tok == token.DEFINE && relationshipClockSelector(stmt.Rhs[0], "c", "directLAN") {
				storeIndex = i
			}
			if len(stmt.Rhs) == 1 {
				call, ok := stmt.Rhs[0].(*ast.CallExpr)
				if ok && relationshipClockSelector(call.Fun, "s", "managedCurrentEndpointProjectionLocked") {
					projectionIndex = i
					if len(call.Args) != 1 || !relationshipClockCall(call.Args[0], "time", "Now") {
						t.Fatal("projection must sample at the call under the store mutex")
					}
				}
			}
		case *ast.ExprStmt:
			if relationshipClockCall(stmt.X, "s", "mu", "Lock") {
				lockIndex = i
			}
		}
	}
	if storeIndex < 0 || lockIndex <= storeIndex || projectionIndex != lockIndex+2 {
		t.Fatal("clock sample is not immediately after locking the actual store")
	}
	unlock, ok := fn.Body.List[lockIndex+1].(*ast.DeferStmt)
	if !ok || !relationshipClockCall(unlock.Call, "s", "mu", "Unlock") {
		t.Fatal("store unlock must be deferred past the projection")
	}
	var samples, stores, locks, unlocks int
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.CallExpr:
			if relationshipClockSelector(n.Fun, "time", "Now") {
				samples++
			}
			if relationshipClockSelector(n.Fun, "s", "mu", "Lock") {
				locks++
			}
			if relationshipClockSelector(n.Fun, "s", "mu", "Unlock") {
				unlocks++
			}
		case *ast.AssignStmt:
			for _, lhs := range n.Lhs {
				if relationshipClockSelector(lhs, "s") {
					stores++
				}
			}
		}
		return true
	})
	if samples != 1 || stores != 1 || locks != 1 || unlocks != 1 {
		t.Fatal("relationship observation resamples, replaces the store or releases its lock early")
	}
}

func relationshipClockSelector(expr ast.Expr, path ...string) bool {
	if len(path) == 1 {
		id, ok := expr.(*ast.Ident)
		return ok && id.Name == path[0]
	}
	selector, ok := expr.(*ast.SelectorExpr)
	return ok && selector.Sel.Name == path[len(path)-1] && relationshipClockSelector(selector.X, path[:len(path)-1]...)
}

func relationshipClockCall(expr ast.Expr, path ...string) bool {
	call, ok := expr.(*ast.CallExpr)
	return ok && len(call.Args) == 0 && relationshipClockSelector(call.Fun, path...)
}

// Both behavioral cases use only the inert owned-file fixture. There is no
// Core.Open, Node, transport, provider, goroutine, sleep or clock injection into
// the production helper. A sequential observation is not a concurrency proof.
func TestResourceGrantRelationshipSerializedObservationAndRollback(t *testing.T) {
	for _, rollback := range []bool{false, true} {
		name := "later_serialized_sample"
		if rollback {
			name = "genuine_rollback_stays_denied"
		}
		t.Run(name, func(t *testing.T) {
			c, peer := resourceGrantFixture(t)
			c.op.Lock()
			defer c.op.Unlock()
			s := c.directLAN
			before, err := os.ReadFile(s.path)
			if err != nil {
				t.Fatal(err)
			}
			beforeState, beforeGrants := resourceDigest(s.state), resourceDigest(c.resourceGrants.state)
			var expected resourcegrant.Relationship
			var observed time.Time
			err = c.withResourceInspectionState(func(*resourcePathBinding) error {
				s.mu.Lock()
				defer s.mu.Unlock()
				observed = time.Now()
				cfg, err := s.managedCurrentEndpointProjectionLocked(observed)
				if err != nil {
					return err
				}
				binding, err := cfg.PairContexts[peer].Binding()
				if err != nil {
					return err
				}
				expected = resourcegrant.Relationship{Backend: resourcegrant.Backend, TargetKey: cfg.Identity.PublicKey(), PeerKey: peer, PairBinding: binding}
				if rollback {
					_, err = s.managedCurrentEndpointProjectionLocked(observed.Add(-time.Nanosecond))
					if !errors.Is(err, directlan.ErrRecovery) || !s.recovery {
						t.Fatal("genuine lower wall observation did not latch recovery")
					}
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var got resourcegrant.Relationship
			err = c.withResourceInspectionState(func(*resourcePathBinding) error {
				var err error
				got, err = c.resourceGrantRelationship(peer)
				return err
			})
			if rollback {
				if err == nil || got != (resourcegrant.Relationship{}) {
					t.Fatal("fresh sample cleared genuine rollback denial")
				}
			} else if err != nil || got != expected {
				t.Fatal("later serialized lookup changed the original relationship", err)
			}
			s.mu.Lock()
			recovery, wall, afterState := s.recovery, s.endpointObservedAt, resourceDigest(s.state)
			s.mu.Unlock()
			if recovery != rollback || wall.Before(observed.UTC()) || afterState != beforeState || resourceDigest(c.resourceGrants.state) != beforeGrants {
				t.Fatal("observation changed authority, lost recovery or moved the high-water backward")
			}
			after, err := os.ReadFile(s.path)
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("relationship observation rewrote the protected store", err)
			}
			if _, err := os.Stat(resourceGrantStatePath(c.dir)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("relationship observation created grant state", err)
			}
		})
	}
}
