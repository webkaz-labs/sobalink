package directlan

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourcegrant"
)

// Source checks document the audited non-reentrant gate. They are staged source
// only until the exact runtime/test inventory receives its execution gate.
func TestManagementFinalGatesContainOnlyLeafCalls(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "resource_management.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	forbidden := map[string]bool{
		"currentLocked": true, "validFlowLocked": true, "sessionIdentity": true,
		"applicationPeer": true, "trafficOpen": true, "open": true, "current": true,
		"OverlayAddress": true, "Done": true, "Err": true, "Deadline": true,
		"borrow": true, "release": true, "Write": true, "Read": true,
		"Marshal": true, "Encode": true, "Binding": true,
	}
	check := func(body ast.Node) {
		ast.Inspect(body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := ""
			switch function := call.Fun.(type) {
			case *ast.Ident:
				name = function.Name
			case *ast.SelectorExpr:
				name = function.Sel.Name
			}
			if forbidden[name] || name == "admit" {
				t.Errorf("prohibited call %s inside final gate", name)
			}
			return true
		})
	}
	gates := 0
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if function.Name.Name == "transportLeafLocked" || function.Name.Name == "currentLeafLocked" {
			check(function.Body)
		}
		if function.Name.Name != "AdmitProvider" && function.Name.Name != "PrepareReply" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "admit" {
				return true
			}
			if len(call.Args) != 1 {
				t.Fatal("unexpected final admission signature")
			}
			callback, ok := call.Args[0].(*ast.FuncLit)
			if !ok {
				t.Fatal("final gate gained a generic callback")
			}
			gates++
			check(callback.Body)
			return false
		})
	}
	if gates != 2 {
		t.Fatalf("expected two independent concrete gates, got %d", gates)
	}
}

func TestManagementClientSingleRequestAndFixedBudgetSource(t *testing.T) {
	if managementExchangeTimeout != 15*time.Second {
		t.Fatal("client exchange budget changed")
	}
	file, err := parser.ParseFile(token.NewFileSet(), "resource_management_client.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	applicationWrites, dials := 0, 0
	ast.Inspect(file, func(node ast.Node) bool {
		switch node.(type) {
		case *ast.ForStmt, *ast.RangeStmt:
			t.Error("management client gained replay loop")
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if selector.Sel.Name == "dialInspectionPeer" {
			dials++
		}
		if selector.Sel.Name == "Write" && len(call.Args) == 1 {
			if argument, ok := call.Args[0].(*ast.Ident); ok && argument.Name == "frame" {
				applicationWrites++
			}
		}
		if selector.Sel.Name == "HelloRequest" || selector.Sel.Name == "ReadHelloResponse" || strings.HasPrefix(selector.Sel.Name, "DialContext") {
			t.Error("management client gained legacy or OS-dial fallback")
		}
		return true
	})
	if dials != 1 || applicationWrites != 1 {
		t.Fatal("management client must dial once and send one application request")
	}
}

func TestManagementClientCancelledBeforeTransport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := managementInertOwner(t, resourcegrant.ManagementProtocolVersion, managementTestRequest(resourcegrant.Inspect))
	expected := c.relationship
	expected.TargetKey, expected.PeerKey = expected.PeerKey, expected.TargetKey
	// A zero Node would panic if cancellation reached CapturePeer or dialing.
	if _, err := (&Node{}).ManageRemote(ctx, expected, managementTestRequest(resourcegrant.Inspect)); err != ErrUnavailable {
		t.Fatalf("cancelled client advanced to transport: %v", err)
	}
}
