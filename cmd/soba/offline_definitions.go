package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/webkaz-labs/sobalink/internal/core"
	"github.com/webkaz-labs/sobalink/internal/webui"
)

func offlineDefinitionCLIAllowed(command string, args []string) bool {
	if command == "favorites" {
		return len(args) == 0 || args[0] == "list" || args[0] == "add" || args[0] == "remove" || args[0] == "help" || args[0] == "--help" || args[0] == "-h"
	}
	if command == "profile" || command == "rules" || command == "settings" || command == "rustdesk" {
		return true
	}
	if len(args) == 0 {
		return command == "service" || command == "group"
	}
	if args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return command == "service" || command == "group"
	}
	return command == "service" && (args[0] == "save" || args[0] == "show" || args[0] == "delete") || command == "group" && (args[0] == "list" || args[0] == "save")
}

func offlineDefinitionCall(ctx context.Context, dir, raw string, result any) error {
	limits, err := core.ReadLocalControlLimits(dir)
	if err != nil {
		return err
	}
	if int64(len(raw)) > limits.CommandBytes {
		return errors.New("offline command exceeds the selected finite input budget")
	}
	var command webui.Command
	if err := json.Unmarshal([]byte(raw), &command); err != nil {
		return err
	}
	value, err := core.OfflineDefinitionCommand(ctx, core.Options{Directory: dir, Version: version, SkipNetworkStart: true}, command)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if int64(len(encoded)) > limits.ResponseBytes {
		return errors.New("offline result exceeds the selected finite response budget")
	}
	return json.Unmarshal(encoded, result)
}
