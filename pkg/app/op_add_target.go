package app

import (
	"github.com/bwagner5/triad/pkg/registry"

	"github.com/aws/lightsailctl/pkg/names"
)

// addTargetOp implements `lightsailctl app add-target`. It bootstraps an
// existing Lightsail instance as an additional deployment target for an
// app/env pair that was already created via `app create` or `deploy`.
func addTargetOp(s *store) registry.Operation {
	return registry.Operation{
		Name:  "add-target",
		Short: "add an instance as a deployment target",
		Fields: []registry.Field{
			{Flag: "name", Short: "n", Label: "App name", Help: "app name",
				Required: true, Prefill: names.DefaultAppName, Validate: names.ValidateLabel},
			{Flag: "env", Short: "e", Label: "Environment", Help: "environment",
				Required: true, Default: "dev", Validate: names.ValidateLabel},
			{Flag: "instance", Short: "i", Label: "Lightsail instance",
				Help: "target Lightsail instance to add", Required: true,
				Suggest: instanceSuggest(s)},
			{Flag: "agent-path", Label: "Agent binary",
				Help: "linux/amd64 lightsailctl binary to scp to the instance",
				File: true, When: needsAgentBinaryPrompt},
			{Flag: "region", Help: "AWS region (auto-filled from --instance)",
				Wizard: registry.BoolPtr(false)},
		},
		Steps: []registry.Step{},
	}
}
