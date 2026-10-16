// Package assets embeds the image build contexts, agent configs and model
// metadata shipped with kota.
package assets

import "embed"

// Images holds one build context per image under images/<name>/.
//
//go:embed images
var Images embed.FS

// Agents holds each agent's base config under agents/<agent>/. It is
// versioned here so the agent can't change its own permissions or
// providers.
//
//go:embed agents
var Agents embed.FS

// Models is the model metadata merged with the gateway's model list.
//
//go:embed models.yaml
var Models []byte
