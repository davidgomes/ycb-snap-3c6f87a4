//                           _       _
// __      _____  __ ___   ___  __ _| |_ ___
// \ \ /\ / / _ \/ _` \ \ / / |/ _` | __/ _ \
//  \ V  V /  __/ (_| |\ V /| | (_| | ||  __/
//   \_/\_/ \___|\__,_| \_/ |_|\__,_|\__\___|
//
//  Copyright © 2016 - 2026 Weaviate B.V. All rights reserved.
//
//  CONTACT: hello@weaviate.io
//

//go:build integrationTest

package lsmkv

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-openapi/strfmt"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/storobj"
)

type objectDigestOp struct {
	kind       string // "put", "delete", "flush" or "switch"
	key        string
	docID      uint64
	updateTime int64
}

func digestPut(key string, docID uint64, updateTime int64) objectDigestOp {
	return objectDigestOp{kind: "put", key: key, docID: docID, updateTime: updateTime}
}

func digestDelete(key string) objectDigestOp {
	return objectDigestOp{kind: "delete", key: key}
}

var (
	digestFlush = objectDigestOp{kind: "flush"}
	// digestSwitch parks the active memtable as the flushing one without
	// flushing it, like an in-flight FlushAndSwitch.
	digestSwitch = objectDigestOp{kind: "switch"}
)

// objectDigestKey maps a short test name to a 16-byte objects-bucket key.
func objectDigestKey(name string) []byte {
	k := make([]byte, 16)
	copy(k, name)
	return k
}

func marshalDigestTestObject(t *testing.T, key string, docID uint64, updateTime int64) []byte {
	t.Helper()
	obj := storobj.New(docID)
	obj.SetID(strfmt.UUID(fmt.Sprintf("00000000-0000-0000-0000-%012x", docID)))
	obj.Object.LastUpdateTimeUnix = updateTime
	obj.Object.Class = "DigestTest"
	obj.Vector = make([]float32, 1536)
	for i := range obj.Vector {
		obj.Vector[i] = float32(i) + float32(docID)
	}
	v, err := obj.MarshalBinary()
	require.NoError(t, err)
	return v
}

// TestBucketApplyToObjectDigests_NewestVersionOnly pins that every live key is
// reported exactly once with its newest update time, whichever of memtable and
// disk holds that version. The hashtree XOR-aggregates these digests, so a
// stale disk version reported next to (or instead of) a memtable version
// leaves the leaf permanently diverged.
func TestBucketApplyToObjectDigests_NewestVersionOnly(t *testing.T) {
	ctx := context.Background()

	journeys := []struct {
		name string
		ops  []objectDigestOp
		want map[string][]int64
	}{
		{
			name: "disk only",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush},
			want: map[string][]int64{"a": {100}},
		},
		{
			name: "memtable only",
			ops:  []objectDigestOp{digestPut("a", 1, 100)},
			want: map[string][]int64{"a": {100}},
		},
		{
			name: "memtable update keeps docID",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestPut("a", 1, 200)},
			want: map[string][]int64{"a": {200}},
		},
		{
			name: "memtable update with new docID",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestPut("a", 2, 200)},
			want: map[string][]int64{"a": {200}},
		},
		{
			name: "memtable delete shadows disk",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestDelete("a")},
			want: map[string][]int64{},
		},
		{
			name: "memtable delete then re-put with new docID",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestDelete("a"), digestPut("a", 2, 300),
			},
			want: map[string][]int64{"a": {300}},
		},
		{
			name: "memtable delete then re-put with same docID",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestDelete("a"), digestPut("a", 1, 300),
			},
			want: map[string][]int64{"a": {300}},
		},
		{
			name: "memtable put then delete of disk key",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestPut("a", 2, 200), digestDelete("a"),
			},
			want: map[string][]int64{},
		},
		{
			name: "memtable delete of key absent on disk",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestDelete("b")},
			want: map[string][]int64{"a": {100}},
		},
		{
			name: "disk delete in newer segment",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestDelete("a"), digestFlush},
			want: map[string][]int64{},
		},
		{
			name: "disk update across segments with new docID",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestPut("a", 2, 200), digestFlush},
			want: map[string][]int64{"a": {200}},
		},
		{
			name: "disk re-put after disk delete, then memtable update with new docID",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush,
				digestDelete("a"), digestFlush,
				digestPut("a", 2, 200), digestFlush,
				digestPut("a", 3, 300),
			},
			want: map[string][]int64{"a": {300}},
		},
		{
			name: "mixed keys across memtable and segments",
			ops: []objectDigestOp{
				digestPut("a", 1, 100),
				digestPut("b", 2, 100),
				digestPut("c", 3, 100),
				digestPut("e", 4, 100),
				digestFlush,
				digestPut("f", 5, 150),
				digestPut("b", 6, 160),
				digestFlush,
				digestPut("b", 7, 200),
				digestDelete("c"),
				digestPut("d", 8, 200),
				digestPut("e", 4, 200),
				digestDelete("f"),
			},
			want: map[string][]int64{"a": {100}, "b": {200}, "d": {200}, "e": {200}},
		},
		{
			name: "flushing memtable tombstone shadows disk",
			ops:  []objectDigestOp{digestPut("a", 1, 100), digestFlush, digestDelete("a"), digestSwitch},
			want: map[string][]int64{},
		},
		{
			name: "active tombstone shadows flushing update and disk",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestPut("a", 2, 200), digestSwitch, digestDelete("a"),
			},
			want: map[string][]int64{},
		},
		{
			name: "active update over flushing update over disk with new docIDs",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestPut("a", 2, 200), digestSwitch, digestPut("a", 3, 300),
			},
			want: map[string][]int64{"a": {300}},
		},
		{
			name: "active re-put over flushing tombstone over disk",
			ops: []objectDigestOp{
				digestPut("a", 1, 100), digestFlush, digestDelete("a"), digestSwitch, digestPut("a", 2, 300),
			},
			want: map[string][]int64{"a": {300}},
		},
	}

	for _, mode := range digestCursorModes {
		for _, j := range journeys {
			t.Run(mode.name+"/"+j.name, func(t *testing.T) {
				opts := append(append([]BucketOption{}, mode.opts...), WithSecondaryIndices(1))
				b := newReusableTestBucket(t, ctx, opts...)
				defer b.Shutdown(ctx)

				// like the shard, deletes carry the docID secondary key of the version they remove
				currentDocID := map[string]uint64{}
				flushInFlight := false
				for _, op := range j.ops {
					switch op.kind {
					case "put":
						v := marshalDigestTestObject(t, op.key, op.docID, op.updateTime)
						require.NoError(t, b.Put(objectDigestKey(op.key), v,
							WithSecondaryKey(0, []byte(fmt.Sprintf("doc-%d", op.docID)))))
						currentDocID[op.key] = op.docID
					case "delete":
						require.NoError(t, b.Delete(objectDigestKey(op.key),
							WithSecondaryKey(0, []byte(fmt.Sprintf("doc-%d", currentDocID[op.key])))))
						delete(currentDocID, op.key)
					case "flush":
						require.NoError(t, b.FlushAndSwitch())
					case "switch":
						switched, err := b.atomicallySwitchMemtable(b.createNewActiveMemtable)
						require.NoError(t, err)
						require.True(t, switched)
						flushInFlight = true
					}
				}
				if flushInFlight {
					require.NotNil(t, b.flushing)
					defer completeInFlightFlush(t, b)
				}

				afterInMemCalls := 0
				got := map[string][]int64{}
				err := b.ApplyToObjectDigests(ctx, func() { afterInMemCalls++ },
					func(uuidBytes []byte, updateTime int64) error {
						name := string(uuidBytes[:len(uuidBytes)-countTrailingZeros(uuidBytes)])
						got[name] = append(got[name], updateTime)
						return nil
					})
				require.NoError(t, err)
				require.Equal(t, 1, afterInMemCalls)
				require.Equal(t, j.want, got)
			})
		}
	}
}

// completeInFlightFlush finishes a flush started by atomicallySwitchMemtable
// the way FlushAndSwitch does.
func completeInFlightFlush(t *testing.T, b *Bucket) {
	t.Helper()
	b.waitForZeroWriters(b.flushing)
	segmentPath, err := b.flushing.flush()
	require.NoError(t, err)
	seg, err := b.disk.initAndPrecomputeNewSegment(segmentPath)
	require.NoError(t, err)
	require.NoError(t, b.atomicallyAddDiskSegmentAndRemoveFlushing(seg))
}

func countTrailingZeros(b []byte) int {
	n := 0
	for i := len(b) - 1; i >= 0 && b[i] == 0; i-- {
		n++
	}
	return n
}
