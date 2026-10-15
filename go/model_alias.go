package main

import "strings"

// Claude Code only accepts model IDs that look like Claude models, so CPA
// rewrites every other catalog entry to claude-fable-5-dd- plus the original ID
// reversed. Routing decodes that ID again, but a usage record can still capture
// the client-requested name. Left as-is it hides the real model in every
// dashboard table and makes price lookup fail, because prices are keyed by the
// upstream model name.
const claudeCloakedModelPrefix = "claude-fable-5-dd-"

// decodeClaudeCloakedModel restores the upstream model ID behind a cloaked
// Claude Code catalog entry. A thinking suffix in model(value) form stays
// outside the reversed segment, matching how CPA encodes it. Names without the
// prefix are returned unchanged, so the decode is safe whatever the client
// sends.
func decodeClaudeCloakedModel(model string) string {
	name := strings.TrimSpace(model)
	if !strings.HasPrefix(name, claudeCloakedModelPrefix) {
		return model
	}
	base, suffix, hasSuffix := splitCloakedModelSuffix(name)
	if !strings.HasPrefix(base, claudeCloakedModelPrefix) {
		return model
	}
	encoded := base[len(claudeCloakedModelPrefix):]
	if encoded == "" {
		return model
	}
	reversed := reverseModelName(encoded)
	if hasSuffix {
		return reversed + "(" + suffix + ")"
	}
	return reversed
}

// A cloaked ID never contains "(" because the prefix and the reversed upstream
// ID both come from catalog names. Requiring the closing ")" keeps a genuine
// suffix out of the reversed segment.
func splitCloakedModelSuffix(model string) (base, suffix string, hasSuffix bool) {
	lastOpen := strings.LastIndex(model, "(")
	if lastOpen == -1 || !strings.HasSuffix(model, ")") {
		return model, "", false
	}
	return model[:lastOpen], model[lastOpen+1 : len(model)-1], true
}

func reverseModelName(id string) string {
	runes := []rune(id)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}
