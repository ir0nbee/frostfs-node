package meta_test

import (
	"os"
	"strconv"
	"testing"

	objectV2 "github.com/TrueCloudLab/frostfs-api-go/v2/object"
	meta "github.com/TrueCloudLab/frostfs-node/pkg/local_object_storage/metabase"
	"github.com/TrueCloudLab/frostfs-sdk-go/checksum"
	checksumtest "github.com/TrueCloudLab/frostfs-sdk-go/checksum/test"
	cid "github.com/TrueCloudLab/frostfs-sdk-go/container/id"
	cidtest "github.com/TrueCloudLab/frostfs-sdk-go/container/id/test"
	"github.com/TrueCloudLab/frostfs-sdk-go/object"
	objectSDK "github.com/TrueCloudLab/frostfs-sdk-go/object"
	oid "github.com/TrueCloudLab/frostfs-sdk-go/object/id"
	oidtest "github.com/TrueCloudLab/frostfs-sdk-go/object/id/test"
	usertest "github.com/TrueCloudLab/frostfs-sdk-go/user/test"
	"github.com/TrueCloudLab/frostfs-sdk-go/version"
	"github.com/TrueCloudLab/tzhash/tz"
	"github.com/stretchr/testify/require"
)

type epochState struct{ e uint64 }

func (s epochState) CurrentEpoch() uint64 {
	if s.e != 0 {
		return s.e
	}

	return 0
}

// saves "big" object in DB.
func putBig(db *meta.DB, obj *object.Object) error {
	return metaPut(db, obj, nil)
}

func testSelect(t *testing.T, db *meta.DB, cnr cid.ID, fs object.SearchFilters, exp ...oid.Address) {
	res, err := metaSelect(db, cnr, fs)
	require.NoError(t, err)
	require.Len(t, res, len(exp))

	for i := range exp {
		require.Contains(t, res, exp[i])
	}
}

func newDB(t testing.TB, opts ...meta.Option) *meta.DB {
	path := t.Name()

	bdb := meta.New(
		append([]meta.Option{
			meta.WithPath(path),
			meta.WithPermissions(0600),
			meta.WithEpochState(epochState{}),
		}, opts...)...,
	)

	require.NoError(t, bdb.Open(false))
	require.NoError(t, bdb.Init())

	t.Cleanup(func() {
		bdb.Close()
		os.Remove(bdb.DumpInfo().Path)
	})

	return bdb
}

func generateObject(t testing.TB) *object.Object {
	return generateObjectWithCID(t, cidtest.ID())
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

func checkExpiredObjects(t *testing.T, db *meta.DB, f func(exp, nonExp *objectSDK.Object)) {
	expObj := generateObject(t)
	setExpiration(expObj, currEpoch-1)

	require.NoError(t, metaPut(db, expObj, nil))

	nonExpObj := generateObject(t)
	setExpiration(nonExpObj, currEpoch)

	require.NoError(t, metaPut(db, nonExpObj, nil))

	f(expObj, nonExpObj)
}

func setExpiration(o *objectSDK.Object, epoch uint64) {
	var attr objectSDK.Attribute

	attr.SetKey(objectV2.SysAttributeExpEpoch)
	attr.SetValue(strconv.FormatUint(epoch, 10))

	o.SetAttributes(append(o.Attributes(), attr)...)
}
