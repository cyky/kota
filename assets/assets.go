// Package assets embeds the image build contexts shipped with localllm.
package assets

import "embed"

// Images holds one build context per image under images/<name>/.
//
//go:embed images
var Images embed.FS
