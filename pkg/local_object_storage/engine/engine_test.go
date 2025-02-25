package engine

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/blobstor"
	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/blobstor/blobovniczatree"
	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/blobstor/fstree"
	meta "github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/metabase"
	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/pilorama"
	"github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/shard"
	"github.com/TrueCloudLab/frostfs-node/pkg/util/logger"
	"github.com/TrueCloudLab/frostfs-sdk-go/checksum"
	checksumtest "github.com/TrueCloudLab/frostfs-sdk-go/checksum/test"
	cid "github.com/TrueCloudLab/frostfs-sdk-go/container/id"
	cidtest "github.com/TrueCloudLab/frostfs-sdk-go/container/id/test"
	"github.com/TrueCloudLab/frostfs-sdk-go/object"
	oidtest "github.com/TrueCloudLab/frostfs-sdk-go/object/id/test"
	usertest "github.com/TrueCloudLab/frostfs-sdk-go/user/test"
	"github.com/TrueCloudLab/frostfs-sdk-go/version"
	"github.com/TrueCloudLab/hrw"
	"github.com/TrueCloudLab/tzhash/tz"
	"github.com/panjf2000/ants/v2"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

type epochState struct{}

func (s epochState) CurrentEpoch() uint64 {
	return 0
}

func BenchmarkExists(b *testing.B) {
	b.Run("2 shards", func(b *testing.B) {
		benchmarkExists(b, 2)
	})
	b.Run("4 shards", func(b *testing.B) {
		benchmarkExists(b, 4)
	})
	b.Run("8 shards", func(b *testing.B) {
		benchmarkExists(b, 8)
	})
}

func benchmarkExists(b *testing.B, shardNum int) {
	shards := make([]*shard.Shard, shardNum)
	for i := 0; i < shardNum; i++ {
		shards[i] = testNewShard(b, i)
	}

	e := testNewEngineWithShards(shards...)
	b.Cleanup(func() {
		_ = e.Close()
		_ = os.RemoveAll(b.Name())
	})

	addr := oidtest.Address()
	for i := 0; i < 100; i++ {
		obj := generateObjectWithCID(b, cidtest.ID())
		err := Put(e, obj)
		if err != nil {
			b.Fatal(err)
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ok, err := e.exists(addr)
		if err != nil || ok {
			b.Fatalf("%t %v", ok, err)
		}
	}
}

func testNewEngineWithShards(shards ...*shard.Shard) *StorageEngine {
	engine := New()

	for _, s := range shards {
		pool, err := ants.NewPool(10, ants.WithNonblocking(true))
		if err != nil {
			panic(err)
		}

		engine.shards[s.ID().String()] = hashedShard{
			shardWrapper: shardWrapper{
				errorCount: atomic.NewUint32(0),
				Shard:      s,
			},
			hash: hrw.Hash([]byte(s.ID().String())),
		}
		engine.shardPools[s.ID().String()] = pool
	}

	return engine
}

func newStorages(root string, smallSize uint64) []blobstor.SubStorage {
	return []blobstor.SubStorage{
		{
			Storage: blobovniczatree.NewBlobovniczaTree(
				blobovniczatree.WithRootPath(filepath.Join(root, "blobovnicza")),
				blobovniczatree.WithBlobovniczaShallowDepth(1),
				blobovniczatree.WithBlobovniczaShallowWidth(1),
				blobovniczatree.WithPermissions(0700)),
			Policy: func(_ *object.Object, data []byte) bool {
				return uint64(len(data)) < smallSize
			},
		},
		{
			Storage: fstree.New(
				fstree.WithPath(root),
				fstree.WithDepth(1)),
		},
	}
}

func testNewShard(t testing.TB, id int) *shard.Shard {
	sid, err := generateShardID()
	require.NoError(t, err)

	s := shard.New(
		shard.WithID(sid),
		shard.WithLogger(&logger.Logger{Logger: zap.L()}),
		shard.WithBlobStorOptions(
			blobstor.WithStorages(
				newStorages(filepath.Join(t.Name(), fmt.Sprintf("%d.blobstor", id)),
					1<<20))),
		shard.WithPiloramaOptions(pilorama.WithPath(filepath.Join(t.Name(), fmt.Sprintf("%d.pilorama", id)))),
		shard.WithMetaBaseOptions(
			meta.WithPath(filepath.Join(t.Name(), fmt.Sprintf("%d.metabase", id))),
			meta.WithPermissions(0700),
			meta.WithEpochState(epochState{}),
		))

	require.NoError(t, s.Open())
	require.NoError(t, s.Init())

	return s
}

func testEngineFromShardOpts(t *testing.T, num int, extraOpts []shard.Option) *StorageEngine {
	engine := New()
	for i := 0; i < num; i++ {
		_, err := engine.AddShard(append([]shard.Option{
			shard.WithBlobStorOptions(
				blobstor.WithStorages(
					newStorages(filepath.Join(t.Name(), fmt.Sprintf("blobstor%d", i)),
						1<<20)),
			),
			shard.WithMetaBaseOptions(
				meta.WithPath(filepath.Join(t.Name(), fmt.Sprintf("metabase%d", i))),
				meta.WithPermissions(0700),
				meta.WithEpochState(epochState{}),
			),
			shard.WithPiloramaOptions(
				pilorama.WithPath(filepath.Join(t.Name(), fmt.Sprintf("pilorama%d", i)))),
		}, extraOpts...)...)
		require.NoError(t, err)
	}

	require.NoError(t, engine.Open())
	require.NoError(t, engine.Init())

	return engine
}

func generateObjectWithCID(t testing.TB, cnr cid.ID) *object.Object {
	var ver version.Version
	ver.SetMajor(2)
	ver.SetMinor(1)

	csum := checksumtest.Checksum()

	var csumTZ checksum.Checksum
	csumTZ.SetTillichZemor(tz.Sum(csum.Value()))

	obj := object.New()
	obj.SetID(oidtest.ID())
	obj.SetOwnerID(usertest.ID())
	obj.SetContainerID(cnr)
	obj.SetVersion(&ver)
	obj.SetPayloadChecksum(csum)
	obj.SetPayloadHomomorphicHash(csumTZ)
	obj.SetPayload([]byte{1, 2, 3, 4, 5})

	return obj
}

func addAttribute(obj *object.Object, key, val string) {
	var attr object.Attribute
	attr.SetKey(key)
	attr.SetValue(val)

	attrs := obj.Attributes()
	attrs = append(attrs, attr)
	obj.SetAttributes(attrs...)
}

func testNewEngineWithShardNum(t *testing.T, num int) *StorageEngine {
	shards := make([]*shard.Shard, 0, num)

	for i := 0; i < num; i++ {
		shards = append(shards, testNewShard(t, i))
	}

	return testNewEngineWithShards(shards...)
}
