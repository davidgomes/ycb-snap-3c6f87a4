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
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/cyclemanager"
	"github.com/weaviate/weaviate/entities/lsmkv"
)

const digestTestPrefixLen = 42

var digestValueSizes = []int{0, 1, 10, 41, 42, 43, 100, 4097, 20_000}

func digestTestValue(key, round, size int) []byte {
	v := make([]byte, size)
	for i := range v {
		v[i] = byte(key*31 + round*7 + i)
	}
	return v
}

func digestTestKey(i int) []byte {
	return []byte(fmt.Sprintf("key-%04d", i))
}

type digestReadMode struct {
	name  string
	pread bool
}

var digestReadModes = []digestReadMode{
	{name: "mmap", pread: false},
	{name: "pread", pread: true},
}

func newDigestTestBucket(t *testing.T, ctx context.Context, pread bool, secondaryIndices uint16) *Bucket {
	t.Helper()
	dir := t.TempDir()
	opts := []BucketOption{WithStrategy(StrategyReplace), WithPread(pread)}
	if secondaryIndices > 0 {
		opts = append(opts, WithSecondaryIndices(secondaryIndices))
	}
	b, err := NewBucketCreator().NewBucket(ctx, dir, dir, nullLogger(), nil,
		cyclemanager.NewCallbackGroupNoop(), cyclemanager.NewCallbackGroupNoop(), opts...)
	require.NoError(t, err)
	b.SetMemtableThreshold(1e9)
	t.Cleanup(func() { b.Shutdown(context.Background()) })
	return b
}

func digestPut(t *testing.T, b *Bucket, key, round, size int, secondaryIndices uint16) {
	t.Helper()
	var opts []SecondaryKeyOption
	for j := 0; j < int(secondaryIndices); j++ {
		opts = append(opts, WithSecondaryKey(j, []byte(fmt.Sprintf("sec-%d-%04d-%d", j, key, round))))
	}
	require.NoError(t, b.Put(digestTestKey(key), digestTestValue(key, round, size), opts...))
}

func digestDelete(t *testing.T, b *Bucket, key int, secondaryIndices uint16) {
	t.Helper()
	var opts []SecondaryKeyOption
	for j := 0; j < int(secondaryIndices); j++ {
		opts = append(opts, WithSecondaryKey(j, []byte(fmt.Sprintf("sec-%d-%04d-del", j, key))))
	}
	require.NoError(t, b.Delete(digestTestKey(key), opts...))
}

// populateDigestTestBucket writes three flushed segments with overlapping keys,
// tombstones and wildly varying value sizes, then leaves some writes in the
// active memtable. It returns the set of keys whose latest version lives in
// the memtable.
func populateDigestTestBucket(t *testing.T, b *Bucket, keys int, secondaryIndices uint16) map[string]struct{} {
	t.Helper()
	sz := func(i, round int) int { return digestValueSizes[(i+round)%len(digestValueSizes)] }

	for i := 0; i < keys; i++ {
		digestPut(t, b, i, 0, sz(i, 0), secondaryIndices)
	}
	require.NoError(t, b.FlushAndSwitch())

	for i := 0; i < keys; i += 2 {
		digestPut(t, b, i, 1, sz(i, 1), secondaryIndices)
	}
	for i := 0; i < keys; i += 5 {
		digestDelete(t, b, i, secondaryIndices)
	}
	require.NoError(t, b.FlushAndSwitch())

	for i := 0; i < keys; i += 3 {
		digestPut(t, b, i, 2, sz(i, 2), secondaryIndices)
	}
	require.NoError(t, b.FlushAndSwitch())

	inMem := map[string]struct{}{}
	for i := 0; i < keys; i += 7 {
		digestPut(t, b, i, 3, 20_000, secondaryIndices)
		inMem[string(digestTestKey(i))] = struct{}{}
	}
	for i := 0; i < keys; i += 11 {
		digestDelete(t, b, i, secondaryIndices)
		delete(inMem, string(digestTestKey(i)))
	}
	return inMem
}

func requireSegmentReadMode(t *testing.T, b *Bucket, pread bool) {
	t.Helper()
	segs, release := b.disk.getConsistentViewOfSegments()
	defer release()
	require.NotEmpty(t, segs)
	for _, s := range segs {
		seg, ok := s.(*segment)
		require.True(t, ok)
		require.Equal(t, !pread, seg.readFromMemory, "unexpected segment read path")
	}
}

type digestCursorStep struct {
	node       *segmentReplaceNode
	err        error
	currOffset uint64
}

func assertDigestStepMatches(t *testing.T, full, digest digestCursorStep, prefixLen int, ctx string) {
	t.Helper()
	if full.err == nil || errors.Is(full.err, lsmkv.Deleted) {
		require.Equal(t, full.err, digest.err, "%s: error", ctx)
	} else {
		require.ErrorIs(t, digest.err, full.err, "%s: error", ctx)
	}
	require.Equal(t, full.currOffset, digest.currOffset, "%s: currOffset", ctx)
	if full.node == nil {
		require.Nil(t, digest.node, "%s: node", ctx)
		return
	}
	require.NotNil(t, digest.node, "%s: node", ctx)
	assert.Equal(t, full.node.offset, digest.node.offset, "%s: node offset", ctx)
	assert.Equal(t, full.node.tombstone, digest.node.tombstone, "%s: tombstone", ctx)
	assert.Equal(t, full.node.primaryKey, digest.node.primaryKey, "%s: key", ctx)
	assert.True(t, bytes.Equal(expectedPrefix(full.node.value, prefixLen), digest.node.value),
		"%s: value prefix", ctx)
	if prefixLen > 0 {
		assert.LessOrEqual(t, cap(digest.node.value), prefixLen, "%s: value allocation", ctx)
	}
	for j := 0; j < int(full.node.secondaryIndexCount); j++ {
		assert.True(t, bytes.Equal(full.node.secondaryKeys[j], digest.node.secondaryKeys[j]),
			"%s: secondary key %d", ctx, j)
	}
}

func snapshotStep(c *segmentCursorReplaceReusable, n *segmentReplaceNode, err error) digestCursorStep {
	step := digestCursorStep{err: err, currOffset: c.currOffset}
	if n != nil {
		cp := segmentReplaceNode{
			tombstone:           n.tombstone,
			value:               copySlice(n.value),
			primaryKey:          copySlice(n.primaryKey),
			secondaryIndexCount: n.secondaryIndexCount,
			offset:              n.offset,
		}
		for _, sk := range n.secondaryKeys {
			cp.secondaryKeys = append(cp.secondaryKeys, copySlice(sk))
		}
		step.node = &cp
	}
	return step
}

func TestSegmentCursorReplaceDigestReusable_MatchesFullCursor(t *testing.T) {
	ctx := context.Background()
	for _, mode := range digestReadModes {
		for _, secondaryIndices := range []uint16{0, 2} {
			for _, prefixLen := range []int{0, 1, digestTestPrefixLen, 30_000} {
				name := fmt.Sprintf("%s/sec=%d/prefix=%d", mode.name, secondaryIndices, prefixLen)
				t.Run(name, func(t *testing.T) {
					b := newDigestTestBucket(t, ctx, mode.pread, secondaryIndices)
					populateDigestTestBucket(t, b, 120, secondaryIndices)
					requireSegmentReadMode(t, b, mode.pread)

					segs, release := b.disk.getConsistentViewOfSegments()
					defer release()
					require.Len(t, segs, 3)

					for segIdx, s := range segs {
						seg := s.(*segment)
						full := seg.newReplaceCursorReusable()
						digest := seg.newReplaceCursorDigestReusable(prefixLen)

						// sequential iteration
						fn, ferr := full.first()
						dn, derr := digest.first()
						steps := 0
						for {
							ctxStr := fmt.Sprintf("seg %d step %d", segIdx, steps)
							assertDigestStepMatches(t, snapshotStep(full, fn, ferr),
								snapshotStep(digest, dn, derr), prefixLen, ctxStr)
							if fn == nil {
								break
							}
							fn, ferr = full.next()
							dn, derr = digest.next()
							steps++
						}
						require.ErrorIs(t, ferr, lsmkv.NotFound)
						require.Greater(t, steps, 0)

						// seek to every existing key, keys in between and past the
						// end, then continue sequentially for a few nodes
						for i := -1; i <= 121; i++ {
							targets := [][]byte{digestTestKey(i), append(digestTestKey(i), 0x00)}
							for _, target := range targets {
								fn, ferr = full.seek(target)
								dn, derr = digest.seek(target)
								for k := 0; k < 3; k++ {
									ctxStr := fmt.Sprintf("seg %d seek %q +%d", segIdx, target, k)
									assertDigestStepMatches(t, snapshotStep(full, fn, ferr),
										snapshotStep(digest, dn, derr), prefixLen, ctxStr)
									if fn == nil {
										break
									}
									fn, ferr = full.next()
									dn, derr = digest.next()
								}
							}
						}
					}
				})
			}
		}
	}
}

type digestKV struct {
	key   []byte
	value []byte
}

func drainReplaceCursor(c *CursorReplace, seek []byte) []digestKV {
	var out []digestKV
	var k, v []byte
	if seek == nil {
		k, v = c.First()
	} else {
		k, v = c.Seek(seek)
	}
	for ; k != nil; k, v = c.Next() {
		out = append(out, digestKV{key: copySlice(k), value: copySlice(v)})
	}
	return out
}

func assertDigestKVsMatch(t *testing.T, full, digest []digestKV, prefixLen int, inMem map[string]struct{}) {
	t.Helper()
	require.Equal(t, len(full), len(digest), "entry count")
	for i := range full {
		require.Equal(t, full[i].key, digest[i].key, "key at %d", i)
		want := expectedPrefix(full[i].value, prefixLen)
		if _, ok := inMem[string(full[i].key)]; ok {
			want = full[i].value
		}
		assert.True(t, bytes.Equal(want, digest[i].value),
			"value for %q: want len %d, got len %d", full[i].key, len(want), len(digest[i].value))
	}
}

func TestBucketCursorReplaceDigest_MatchesFullCursor(t *testing.T) {
	ctx := context.Background()
	const keys = 150

	seekTargets := [][]byte{
		nil,
		digestTestKey(0),
		digestTestKey(1),
		digestTestKey(35),               // tombstoned in segment 2
		digestTestKey(77),               // latest in memtable
		append(digestTestKey(88), 0x00), // between keys
		digestTestKey(keys - 1),         // last key
		digestTestKey(keys + 10),        // past the end
		[]byte("a"),                     // before everything
		append(digestTestKey(keys-1), 0xFF, 0xFF), // just past last
	}

	for _, mode := range digestReadModes {
		for _, secondaryIndices := range []uint16{0, 2} {
			t.Run(fmt.Sprintf("%s/sec=%d", mode.name, secondaryIndices), func(t *testing.T) {
				b := newDigestTestBucket(t, ctx, mode.pread, secondaryIndices)
				inMem := populateDigestTestBucket(t, b, keys, secondaryIndices)
				requireSegmentReadMode(t, b, mode.pread)

				for _, prefixLen := range []int{0, digestTestPrefixLen} {
					for _, target := range seekTargets {
						t.Run(fmt.Sprintf("prefix=%d/seek=%q", prefixLen, target), func(t *testing.T) {
							fullC := b.CursorReplaceReusable()
							full := drainReplaceCursor(fullC, target)
							fullC.Close()

							digestC := b.CursorReplaceDigestReusable(prefixLen)
							digest := drainReplaceCursor(digestC, target)
							digestC.Close()

							assertDigestKVsMatch(t, full, digest, prefixLen, inMem)

							legacyC := b.Cursor()
							legacy := drainReplaceCursor(legacyC, target)
							legacyC.Close()
							assertDigestKVsMatch(t, legacy, digest, prefixLen, inMem)

							onDiskC := b.CursorOnDisk()
							onDisk := drainReplaceCursor(onDiskC, target)
							onDiskC.Close()

							onDiskDigestC := b.CursorOnDiskDigest(prefixLen)
							onDiskDigest := drainReplaceCursor(onDiskDigestC, target)
							onDiskDigestC.Close()

							assertDigestKVsMatch(t, onDisk, onDiskDigest, prefixLen, nil)
						})
					}
				}
			})
		}
	}
}

// TestBucketCursorReplaceDigest_ReusedAcrossSeeks mirrors how async
// replication reuses a single cursor for many range scans: repeated Seek/Next
// on the same digest cursor must keep matching a fresh full cursor.
func TestBucketCursorReplaceDigest_ReusedAcrossSeeks(t *testing.T) {
	ctx := context.Background()
	const keys = 200

	for _, mode := range digestReadModes {
		t.Run(mode.name, func(t *testing.T) {
			b := newDigestTestBucket(t, ctx, mode.pread, 0)
			inMem := populateDigestTestBucket(t, b, keys, 0)

			digestC := b.CursorReplaceDigestReusable(digestTestPrefixLen)
			defer digestC.Close()
			fullC := b.CursorReplaceReusable()
			defer fullC.Close()

			for start := 0; start < keys; start += 13 {
				fk, fv := fullC.Seek(digestTestKey(start))
				dk, dv := digestC.Seek(digestTestKey(start))
				for n := 0; n < 17; n++ {
					require.Equal(t, fk, dk)
					if fk == nil {
						break
					}
					want := expectedPrefix(fv, digestTestPrefixLen)
					if _, ok := inMem[string(fk)]; ok {
						want = fv
					}
					require.True(t, bytes.Equal(want, dv), "value for %q", fk)
					fk, fv = fullC.Next()
					dk, dv = digestC.Next()
				}
			}
		})
	}
}

// TestBucketCursorReplaceDigest_SurvivesCompaction verifies digest cursors see
// the same data before and after segments are compacted together.
func TestBucketCursorReplaceDigest_SurvivesCompaction(t *testing.T) {
	ctx := context.Background()
	for _, mode := range digestReadModes {
		t.Run(mode.name, func(t *testing.T) {
			b := newDigestTestBucket(t, ctx, mode.pread, 0)
			populateDigestTestBucket(t, b, 100, 0)
			require.NoError(t, b.FlushAndSwitch())

			beforeC := b.CursorOnDiskDigest(digestTestPrefixLen)
			before := drainReplaceCursor(beforeC, nil)
			beforeC.Close()

			segmentsBefore := len(b.disk.segments)
			for {
				compacted, err := b.disk.compactOnce(ctx)
				require.NoError(t, err)
				if !compacted {
					break
				}
			}
			require.Less(t, len(b.disk.segments), segmentsBefore, "expected compaction to merge segments")

			c := b.CursorOnDiskDigest(digestTestPrefixLen)
			after := drainReplaceCursor(c, nil)
			c.Close()

			fullC := b.CursorOnDisk()
			full := drainReplaceCursor(fullC, nil)
			fullC.Close()

			assert.Equal(t, before, after)
			assertDigestKVsMatch(t, full, after, digestTestPrefixLen, nil)
		})
	}
}
