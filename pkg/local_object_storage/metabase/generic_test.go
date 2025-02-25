package meta

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/internal/storagetest"
)

func TestGeneric(t *testing.T) {
	defer func() { _ = os.RemoveAll(t.Name()) }()

	var n int
	newMetabase := func(t *testing.T) storagetest.Component {
		n++
		dir := filepath.Join(t.Name(), strconv.Itoa(n))
		return New(
			WithEpochState(epochStateImpl{}),
			WithPath(dir))
	}

	storagetest.TestAll(t, newMetabase)
}
