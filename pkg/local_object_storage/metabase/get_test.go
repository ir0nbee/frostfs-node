package meta_test

import (
	"bytes"
	"fmt"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/TrueCloudLab/frostfs-node/pkg/core/object"
	meta "github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/metabase"
	apistatus "github.com/TrueCloudLab/frostfs-sdk-go/client/status"
	cidtest "github.com/TrueCloudLab/frostfs-sdk-go/container/id/test"
	objectSDK "github.com/TrueCloudLab/frostfs-sdk-go/object"
	oid "github.com/TrueCloudLab/frostfs-sdk-go/object/id"
	oidtest "github.com/TrueCloudLab/frostfs-sdk-go/object/id/test"
	"github.com/stretchr/testify/require"
)

func TestDB_Get(t *testing.T) {
	db := newDB(t, meta.WithEpochState(epochState{currEpoch}))

	raw := generateObject(t)

	// equal fails on diff of <nil> attributes and <{}> attributes,
	/* so we make non empty attribute slice in parent*/
	addAttribute(raw, "foo", "bar")

	t.Run("object not found", func(t *testing.T) {
		_, err := metaGet(db, object.AddressOf(raw), false)
		require.Error(t, err)
	})

	t.Run("put regular object", func(t *testing.T) {
		err := putBig(db, raw)
		require.NoError(t, err)

		newObj, err := metaGet(db, object.AddressOf(raw), false)
		require.NoError(t, err)
		require.Equal(t, raw.CutPayload(), newObj)
	})

	t.Run("put tombstone object", func(t *testing.T) {
		raw.SetType(objectSDK.TypeTombstone)
		raw.SetID(oidtest.ID())

		err := putBig(db, raw)
		require.NoError(t, err)

		newObj, err := metaGet(db, object.AddressOf(raw), false)
		require.NoError(t, err)
		require.Equal(t, raw.CutPayload(), newObj)
	})

	t.Run("put storage group object", func(t *testing.T) {
		raw.SetType(objectSDK.TypeStorageGroup)
		raw.SetID(oidtest.ID())

		err := putBig(db, raw)
		require.NoError(t, err)

		newObj, err := metaGet(db, object.AddressOf(raw), false)
		require.NoError(t, err)
		require.Equal(t, raw.CutPayload(), newObj)
	})

	t.Run("put lock object", func(t *testing.T) {
		raw.SetType(objectSDK.TypeLock)
		raw.SetID(oidtest.ID())

		err := putBig(db, raw)
		require.NoError(t, err)

		newObj, err := metaGet(db, object.AddressOf(raw), false)
		require.NoError(t, err)
		require.Equal(t, raw.CutPayload(), newObj)
	})

	t.Run("put virtual object", func(t *testing.T) {
		cnr := cidtest.ID()
		splitID := objectSDK.NewSplitID()

		parent := generateObjectWithCID(t, cnr)
		addAttribute(parent, "foo", "bar")

		child := generateObjectWithCID(t, cnr)
		child.SetParent(parent)
		idParent, _ := parent.ID()
		child.SetParentID(idParent)
		child.SetSplitID(splitID)

		err := putBig(db, child)
		require.NoError(t, err)

		t.Run("raw is true", func(t *testing.T) {
			_, err = metaGet(db, object.AddressOf(parent), true)
			require.Error(t, err)

			var siErr *objectSDK.SplitInfoError
			require.ErrorAs(t, err, &siErr)
			require.Equal(t, splitID, siErr.SplitInfo().SplitID())

			id1, _ := child.ID()
			id2, _ := siErr.SplitInfo().LastPart()
			require.Equal(t, id1, id2)

			_, ok := siErr.SplitInfo().Link()
			require.False(t, ok)
		})

		newParent, err := metaGet(db, object.AddressOf(parent), false)
		require.NoError(t, err)
		require.True(t, binaryEqual(parent.CutPayload(), newParent))

		newChild, err := metaGet(db, object.AddressOf(child), true)
		require.NoError(t, err)
		require.True(t, binaryEqual(child.CutPayload(), newChild))
	})

	t.Run("get removed object", func(t *testing.T) {
		obj := oidtest.Address()
		ts := oidtest.Address()

		require.NoError(t, metaInhume(db, obj, ts))
		_, err := metaGet(db, obj, false)
		require.ErrorAs(t, err, new(apistatus.ObjectAlreadyRemoved))

		obj = oidtest.Address()

		var prm meta.InhumePrm
		prm.SetAddresses(obj)

		_, err = db.Inhume(prm)
		require.NoError(t, err)
		_, err = metaGet(db, obj, false)
		require.ErrorAs(t, err, new(apistatus.ObjectNotFound))
	})

	t.Run("expired object", func(t *testing.T) {
		checkExpiredObjects(t, db, func(exp, nonExp *objectSDK.Object) {
			gotExp, err := metaGet(db, object.AddressOf(exp), false)
			require.Nil(t, gotExp)
			require.ErrorIs(t, err, meta.ErrObjectIsExpired)

			gotNonExp, err := metaGet(db, object.AddressOf(nonExp), false)
			require.NoError(t, err)
			require.True(t, binaryEqual(gotNonExp, nonExp.CutPayload()))
		})
	})
}

// binary equal is used when object contains empty lists in the structure and
// requre.Equal fails on comparing <nil> and []{} lists.
func binaryEqual(a, b *objectSDK.Object) bool {
	binaryA, err := a.Marshal()
	if err != nil {
		return false
	}

	binaryB, err := b.Marshal()
	if err != nil {
		return false
	}

	return bytes.Equal(binaryA, binaryB)
}

func BenchmarkGet(b *testing.B) {
	numOfObjects := [...]int{
		1,
		10,
		100,
	}

	defer func() {
		_ = os.RemoveAll(b.Name())
	}()

	for _, num := range numOfObjects {
		b.Run(fmt.Sprintf("%d_objects", num), func(b *testing.B) {
			benchmarkGet(b, num)
		})
	}
}

var obj *objectSDK.Object

func benchmarkGet(b *testing.B, numOfObj int) {
	prepareDb := func(batchSize int) (*meta.DB, []oid.Address) {
		db := newDB(b,
			meta.WithMaxBatchSize(batchSize),
			meta.WithMaxBatchDelay(10*time.Millisecond),
		)
		addrs := make([]oid.Address, 0, numOfObj)

		for i := 0; i < numOfObj; i++ {
			raw := generateObject(b)
			addrs = append(addrs, object.AddressOf(raw))

			err := putBig(db, raw)
			require.NoError(b, err)
		}

		return db, addrs
	}

	db, addrs := prepareDb(runtime.NumCPU())

	b.Run("parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			var counter int

			for pb.Next() {
				var getPrm meta.GetPrm
				getPrm.SetAddress(addrs[counter%len(addrs)])
				counter++

				_, err := db.Get(getPrm)
				if err != nil {
					b.Fatal(err)
				}
			}
		})
	})

	require.NoError(b, db.Close())
	require.NoError(b, os.RemoveAll(b.Name()))

	db, addrs = prepareDb(1)

	b.Run("serial", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var getPrm meta.GetPrm
			getPrm.SetAddress(addrs[i%len(addrs)])

			_, err := db.Get(getPrm)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func metaGet(db *meta.DB, addr oid.Address, raw bool) (*objectSDK.Object, error) {
	var prm meta.GetPrm
	prm.SetAddress(addr)
	prm.SetRaw(raw)

	res, err := db.Get(prm)
	return res.Header(), err
}
