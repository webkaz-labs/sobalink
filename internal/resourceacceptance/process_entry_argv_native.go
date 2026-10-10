//go:build resource_process_native

package resourceacceptance

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/webkaz-labs/sobalink/internal/resourceacceptance/processmodel"
)

type processEntryArguments struct {
	binary, directory, root, locale, input, expiry string
	mode                                           processmodel.Mode
	operation                                      processmodel.CLIOperation
	offline, dry                                   bool
}

func processEntryPath(path string) bool {
	if len(path) < 2 || len(path) > processmodel.MaxPathBytes || path[0] != '/' || filepath.Clean(path) != path || strings.Contains(path, "\\") {
		return false
	}
	parts := strings.Split(path[1:], "/")
	if len(parts) > 32 {
		return false
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func processEntryHex(value string, size int) bool {
	if len(value) != size {
		return false
	}
	for _, c := range value {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func processEntryParseArguments(argv []string) (processEntryArguments, bool) {
	var result processEntryArguments
	if _, ok := processmodel.DigestArgv(argv); !ok || len(argv) < 6 || argv[1] != "--state-dir" || argv[3] != "--locale" || !processEntryPath(argv[0]) || !processEntryPath(argv[2]) || (argv[4] != "en" && argv[4] != "ja") {
		return result, false
	}
	result.binary, result.directory, result.locale = argv[0], argv[2], argv[4]
	parent := filepath.Dir(result.directory)
	role := filepath.Base(result.directory)
	if filepath.Base(parent) != "state" || (role != "c" && role != "a" && role != "b") {
		return result, false
	}
	result.root = filepath.Dir(parent)
	if !processEntryPath(result.root) {
		return result, false
	}
	args := argv[5:]
	if args[0] == "run" {
		if result.locale != "en" || len(args) > 2 || len(args) == 2 && args[1] != "--offline" {
			return result, false
		}
		result.mode, result.offline = processmodel.Owner, len(args) == 2
		return result, true
	}
	if args[0] != "--json-errors" {
		return result, false
	}
	args = args[1:]
	if len(args) != 0 && args[0] == "--dry-run" {
		result.dry = true
		args = args[1:]
	}
	result.mode = processmodel.CLI
	if len(args) == 2 && args[1] == "--json" && !result.dry && result.locale == "en" {
		switch args[0] {
		case "status":
			result.operation = processmodel.CLILocalStatus
			return result, true
		case "stop":
			result.operation = processmodel.CLILocalStop
			return result, true
		}
	}
	if len(args) < 3 || args[0] != "resource" {
		return result, false
	}
	if len(args) == 3 && args[1] == "list" && args[2] == "--json" && !result.dry && result.locale == "en" {
		result.operation = processmodel.CLIResourceList
		return result, true
	}
	if args[1] == "grant" && role != "c" && !result.dry && result.locale == "en" {
		switch args[2] {
		case "preview":
			if len(args) != 11 || args[3] != "--management" || args[4] != "--id" || !processEntryHex(args[5], 32) || args[6] != "--peer" || !processEntryHex(args[7], 64) || args[8] != "--expires-at" || args[10] != "--json" {
				return result, false
			}
			expiry, err := time.Parse(time.RFC3339, args[9])
			if err != nil || expiry.Unix() <= 0 || expiry.UTC().Format(time.RFC3339) != args[9] {
				return result, false
			}
			result.expiry, result.operation = args[9], processmodel.CLIManagementGrantPreview
		case "confirm":
			if len(args) != 8 || args[3] != "--management" || args[4] != "--review-file" || args[6] != "--confirm" || args[7] != "--json" || args[5] != filepath.Join(result.root, "inputs", "grant-"+role+".json") || !processEntryPath(args[5]) {
				return result, false
			}
			result.input, result.operation = args[5], processmodel.CLIManagementGrantConfirm
		case "inspect":
			if len(args) != 6 || args[3] != "--id" || !processEntryHex(args[4], 32) || args[5] != "--json" {
				return result, false
			}
			result.operation = processmodel.CLIGrantInspect
		default:
			return result, false
		}
		return result, true
	}
	if args[1] != "group" || role != "c" {
		return result, false
	}
	switch args[2] {
	case "preview":
		if result.locale != "en" || len(args) != 6 || args[3] != "--selection-file" || args[5] != "--json" || !processEntryPath(args[4]) {
			return result, false
		}
		applied, unused := filepath.Join(result.root, "inputs", "selection-applied.json"), filepath.Join(result.root, "inputs", "selection-unused.json")
		if args[4] != applied && (result.dry || args[4] != unused) {
			return result, false
		}
		result.input, result.operation = args[4], processmodel.CLIGroupPreview
		if result.dry {
			result.operation = processmodel.CLIDryRunGroupPreview
		}
	case "apply":
		if result.locale != "en" || (len(args) != 11 && len(args) != 13) || args[3] != "--review-id" || !processEntryHex(args[4], 32) || args[5] != "--revision" || !processEntryHex(args[6], 64) || args[7] != "--execute-peer" || !processEntryHex(args[8], 64) || args[len(args)-2] != "--confirm" || args[len(args)-1] != "--json" {
			return result, false
		}
		if len(args) == 13 && (args[9] != "--execute-peer" || !processEntryHex(args[10], 64) || args[8] == args[10]) || result.dry && len(args) != 13 {
			return result, false
		}
		result.operation = processmodel.CLIGroupApply
		if result.dry {
			result.operation = processmodel.CLIDryRunGroupApply
		}
	case "review":
		if result.locale != "en" || result.dry || len(args) != 5 || args[3] != "current" || args[4] != "--json" {
			return result, false
		}
		result.operation = processmodel.CLIGroupCurrent
	case "status":
		if (len(args) != 5 && len(args) != 6) || args[3] != "--run-id" || !processEntryHex(args[4], 32) || len(args) == 6 && args[5] != "--json" || result.dry && (len(args) != 6 || result.locale != "en") {
			return result, false
		}
		result.operation = processmodel.CLIGroupStatus
		if result.dry {
			result.operation = processmodel.CLIDryRunGroupStatus
		}
	default:
		return result, false
	}
	return result, true
}

func processEntryArgumentsMatch(a processEntryArguments, b processmodel.Bootstrap) bool {
	if a.mode != b.Invocation.Mode || a.directory != string(b.RoleDirectory.Bytes[:b.RoleDirectory.Length]) || a.root != string(b.WorkingDirectory.Bytes[:b.WorkingDirectory.Length]) {
		return false
	}
	if a.mode == processmodel.CLI {
		return b.Invocation.Role == 0 && a.operation == b.Invocation.Operation && (a.expiry == "" || a.expiry == time.Unix(int64(b.Schedule.OriginalGrantExpirySeconds), 0).UTC().Format(time.RFC3339))
	}
	role := "c"
	switch b.Invocation.Role {
	case processmodel.RoleA0:
		role = "a"
	case processmodel.RoleB0:
		role = "b"
	case processmodel.RoleC0, processmodel.RoleC1, processmodel.RoleC2:
	default:
		return false
	}
	return filepath.Base(a.directory) == role && a.offline == (b.Invocation.Role != processmodel.RoleC2)
}
