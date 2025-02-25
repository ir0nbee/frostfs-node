package contractsconfig

import (
	"fmt"

	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/config"
	"github.com/nspcc-dev/neo-go/pkg/util"
)

const (
	subsection = "contracts"
)

// Netmap returns the value of "netmap" config parameter
// from "contracts" section.
//
// Returns zero filled script hash if the value is not set.
// Throws panic if the value is not a 20-byte LE hex-encoded string.
func Netmap(c *config.Config) util.Uint160 {
	return contractAddress(c, "netmap")
}

// Balance returns the value of "balance" config parameter
// from "contracts" section.
//
// Returns zero filled script hash if the value is not set.
// Throws panic if the value is not a 20-byte LE hex-encoded string.
func Balance(c *config.Config) util.Uint160 {
	return contractAddress(c, "balance")
}

// Container returns the value of "container" config parameter
// from "contracts" section.
//
// Returns zero filled script hash if the value is not set.
// Throws panic if the value is not a 20-byte LE hex-encoded string.
func Container(c *config.Config) util.Uint160 {
	return contractAddress(c, "container")
}

// Reputation returnsthe value of "reputation" config parameter
// from "contracts" section.
//
// Returns zero filled script hash if the value is not set.
// Throws panic if the value is not a 20-byte LE hex-encoded string.
func Reputation(c *config.Config) util.Uint160 {
	return contractAddress(c, "reputation")
}

// Proxy returnsthe value of "proxy" config parameter
// from "contracts" section.
//
// Returns zero filled script hash if the value is not set.
// Throws panic if the value is not a 20-byte LE hex-encoded string.
func Proxy(c *config.Config) util.Uint160 {
	return contractAddress(c, "proxy")
}

func contractAddress(c *config.Config, name string) util.Uint160 {
	v := config.String(c.Sub(subsection), name)
	if v == "" {
		return util.Uint160{} // if address is not set, then NNS resolver should be used
	}

	addr, err := util.Uint160DecodeStringLE(v)
	if err != nil {
		panic(fmt.Errorf(
			"can't parse %s contract address %s: %w",
			name,
			v,
			err,
		))
	}

	return addr
}
