package replicatorconfig

import (
	"time"

	"github.com/TrueCloudLab/frostfs-node/cmd/frostfs-node/config"
)

const (
	subsection = "replicator"

	// PutTimeoutDefault is a default timeout of object put request in replicator.
	PutTimeoutDefault = 5 * time.Second
)

// PutTimeout returns the value of "put_timeout" config parameter
// from "replicator" section.
//
// Returns PutTimeoutDefault if the value is not positive duration.
func PutTimeout(c *config.Config) time.Duration {
	v := config.DurationSafe(c.Sub(subsection), "put_timeout")
	if v > 0 {
		return v
	}

	return PutTimeoutDefault
}

// PoolSize returns the value of "pool_size" config parameter
// from "replicator" section.
func PoolSize(c *config.Config) int {
	return int(config.IntSafe(c.Sub(subsection), "pool_size"))
}
