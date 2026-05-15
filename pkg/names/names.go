// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package names

import "math/rand/v2"

var adjectives = []string{
	"cosmic", "lunar", "solar", "stellar", "astral",
	"orbital", "nebula", "galactic", "radiant", "blazing",
	"drifting", "silent", "frozen", "burning", "spinning",
	"distant", "ancient", "bright", "dark", "swift",
	"hollow", "molten", "dusty", "fading", "glowing",
	"crimson", "golden", "violet", "cobalt", "amber",
}

var nouns = []string{
	"comet", "pulsar", "quasar", "nebula", "nova",
	"photon", "meteor", "cosmos", "zenith", "vortex",
	"aurora", "eclipse", "horizon", "solstice", "equinox",
	"crater", "orbit", "flare", "corona", "void",
	"titan", "atlas", "helios", "triton", "oberon",
	"europa", "callisto", "ganymede", "io", "phobos",
}

// Random returns a name like "cosmic-comet" or "swift-pulsar".
func Random() string {
	return adjectives[rand.IntN(len(adjectives))] + "-" + nouns[rand.IntN(len(nouns))]
}
