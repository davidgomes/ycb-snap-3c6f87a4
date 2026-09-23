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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/lsmkv"
	"github.com/weaviate/weaviate/entities/storobj"
)

var digestCursorModes = []struct {
	name        string
	readFromMem bool
	opts        []BucketOption
}{
	{"mmap", true, nil},
	{"pread", false, []BucketOption{WithPread(true), WithMinMMapSize(0)}},
}

var digestCursorValueLens = []int{
	0, 5, storobj.MarshallerV1HeaderLen - 1, storobj.MarshallerV1HeaderLen,
	storobj.MarshallerV1HeaderLen + 1, 300, 5_000, 20_000,
}

var digestCursorPrefixes = []int{-3, 0, 1, storobj.MarshallerV1HeaderLen, 64, 1 << 20}

func digestCursorKey(i int) []byte {
	return []byte(fmt.Sprintf("key-%03d", i))
}

func digestCursorPut(t *testing.T, b *Bucket, secondaryIndexCount uint16, i, sizeIdx int, seed byte) {
	t.Helper()
	val := digestTestValue(digestCursorValueLens[sizeIdx%len(digestCursorValueLens)], seed)
	var opts []SecondaryKeyOption
	if secondaryIndexCount > 0 {
		opts = append(opts, WithSecondaryKey(0, []byte(fmt.Sprintf("sec-%03d", i))))
	}
	require.NoError(t, b.Put(digestCursorKey(i), val, opts...))
}

func digestCursorDelete(t *testing.T, b *Bucket, secondaryIndexCount uint16, i int) {
	t.Helper()
	var opts []SecondaryKeyOption
	if secondaryIndexCount > 0 {
		opts = append(opts, WithSecondaryKey(0, []byte(fmt.Sprintf("sec-%03d", i))))
	}
	require.NoError(t, b.Delete(digestCursorKey(i), opts...))
}

// buildDigestCursorBucket creates two overlapping disk segments with values of
// varied sizes and tombstones, then writes to the active memtable. It returns
// the keys whose newest live version is in the memtable.
func buildDigestCursorBucket(t *testing.T, b *Bucket, secondaryIndexCount uint16) map[string]bool {
	t.Helper()

	for i := 0; i < 60; i++ {
		digestCursorPut(t, b, secondaryIndexCount, i, i, byte(i))
	}
	for i := 0; i < 60; i += 7 {
		digestCursorDelete(t, b, secondaryIndexCount, i)
	}
	require.NoError(t, b.FlushAndSwitch())

	for i := 30; i < 90; i++ {
		digestCursorPut(t, b, secondaryIndexCount, i, i+3, byte(i+100))
	}
	for i := 31; i < 90; i += 5 {
		digestCursorDelete(t, b, secondaryIndexCount, i)
	}
	require.NoError(t, b.FlushAndSwitch())

	inMem := map[string]bool{}
	for i := 70; i < 100; i++ {
		digestCursorPut(t, b, secondaryIndexCount, i, i+5, byte(i+200))
		inMem[string(digestCursorKey(i))] = true
	}
	// shadow live disk values with memtable tombstones and revive a disk tombstone
	for _, i := range []int{40, 45, 72} {
		digestCursorDelete(t, b, secondaryIndexCount, i)
		delete(inMem, string(digestCursorKey(i)))
	}
	digestCursorPut(t, b, secondaryIndexCount, 7, 7, 77)
	inMem[string(digestCursorKey(7))] = true

	return inMem
}

func digestErrKind(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, lsmkv.Deleted):
		return "deleted"
	case errors.Is(err, lsmkv.NotFound):
		return "notfound"
	default:
		return err.Error()
	}
}

func assertDigestCursorStep(t *testing.T, full, digest *segmentCursorReplaceReusable,
	fn *segmentReplaceNode, ferr error, dn *segmentReplaceNode, derr error,
	secondaryIndexCount uint16, prefix int,
) {
	t.Helper()
	require.Equal(t, digestErrKind(ferr), digestErrKind(derr))
	require.Equal(t, fn == nil, dn == nil)
	require.Equal(t, full.currOffset, digest.currOffset, "cursors must sit on the same node")
	if fn == nil {
		return
	}
	assertDigestNodeMatches(t, fn, dn, secondaryIndexCount, prefix)
	if prefix > 0 {
		assert.LessOrEqual(t, cap(dn.value), prefix, "the value remainder must never be allocated")
	}
}

// TestSegmentCursorReplaceDigestReusable_MatchesFullCursor walks every segment
// with a full and a digest reusable cursor in lockstep: node positions, keys,
// tombstones, secondary keys and seek results must be identical, and digest
// values must be the prefix of the full ones.
func TestSegmentCursorReplaceDigestReusable_MatchesFullCursor(t *testing.T) {
	ctx := context.Background()

	for _, mode := range digestCursorModes {
		for _, secondaryIndexCount := range []uint16{0, 1} {
			t.Run(fmt.Sprintf("%s/secondary=%d", mode.name, secondaryIndexCount), func(t *testing.T) {
				opts := append([]BucketOption{}, mode.opts...)
				if secondaryIndexCount > 0 {
					opts = append(opts, WithSecondaryIndices(secondaryIndexCount))
				}
				b := newReusableTestBucket(t, ctx, opts...)
				defer b.Shutdown(ctx)
				buildDigestCursorBucket(t, b, secondaryIndexCount)
				require.Len(t, b.disk.segments, 2)

				for segIdx, sg := range b.disk.segments {
					seg, ok := sg.(*segment)
					require.True(t, ok)
					require.Equal(t, mode.readFromMem, seg.readFromMemory)

					for _, prefix := range digestCursorPrefixes {
						t.Run(fmt.Sprintf("segment=%d/prefix=%d", segIdx, prefix), func(t *testing.T) {
							full := seg.newReplaceCursorReusable()
							digest := seg.newReplaceCursorDigestReusable(prefix)

							fn, ferr := full.first()
							dn, derr := digest.first()
							nodes := 0
							for {
								assertDigestCursorStep(t, full, digest, fn, ferr, dn, derr, secondaryIndexCount, prefix)
								if fn == nil {
									break
								}
								nodes++
								fn, ferr = full.next()
								dn, derr = digest.next()
							}
							assert.Equal(t, full.keyCount(), nodes, "every on-disk node must be visited")

							for _, target := range []string{"", "key-000", "key-007", "key-030", "key-0305", "key-059", "key-089", "zzz"} {
								fn, ferr = full.seek([]byte(target))
								dn, derr = digest.seek([]byte(target))
								for step := 0; step < 4; step++ {
									assertDigestCursorStep(t, full, digest, fn, ferr, dn, derr, secondaryIndexCount, prefix)
									if fn == nil {
										break
									}
									fn, ferr = full.next()
									dn, derr = digest.next()
								}
							}
						})
					}
				}
			})
		}
	}
}

type digestKV struct {
	key, value string
}

func drainDigestKVs(c *CursorReplace) []digestKV {
	defer c.Close()
	var out []digestKV
	for k, v := c.First(); k != nil; k, v = c.Next() {
		out = append(out, digestKV{string(k), string(v)})
	}
	return out
}

func drainDigestKVsFrom(c *CursorReplace, target []byte) []digestKV {
	defer c.Close()
	var out []digestKV
	for k, v := c.Seek(target); k != nil; k, v = c.Next() {
		out = append(out, digestKV{string(k), string(v)})
	}
	return out
}

// truncateDiskValues turns a full-value cursor result into what a digest
// cursor must return: values from disk truncated to prefix, memtable values
// whole.
func truncateDiskValues(in []digestKV, prefix int, inMem map[string]bool) []digestKV {
	if len(in) == 0 {
		return nil
	}
	out := make([]digestKV, len(in))
	for i, kv := range in {
		out[i] = kv
		if !inMem[kv.key] {
			out[i].value = string(expectedDigestValue([]byte(kv.value), prefix))
		}
	}
	return out
}

var digestSeekTargets = []string{"", "key-000", "key-007", "key-040", "key-0455", "key-072", "key-089", "key-099", "zzz"}

func TestCursorReplaceDigestReusable_MatchesCursor(t *testing.T) {
	ctx := context.Background()

	for _, mode := range digestCursorModes {
		t.Run(mode.name, func(t *testing.T) {
			b := newReusableTestBucket(t, ctx, append(append([]BucketOption{}, mode.opts...), WithSecondaryIndices(1))...)
			defer b.Shutdown(ctx)
			inMem := buildDigestCursorBucket(t, b, 1)

			for _, prefix := range digestCursorPrefixes {
				t.Run(fmt.Sprintf("prefix=%d", prefix), func(t *testing.T) {
					want := truncateDiskValues(drainDigestKVs(b.Cursor()), prefix, inMem)
					require.NotEmpty(t, want)
					require.Equal(t, want, drainDigestKVs(b.CursorReplaceDigestReusable(prefix)),
						"First/Next sequence mismatch")

					for _, target := range digestSeekTargets {
						want := truncateDiskValues(drainDigestKVsFrom(b.Cursor(), []byte(target)), prefix, inMem)
						got := drainDigestKVsFrom(b.CursorReplaceDigestReusable(prefix), []byte(target))
						require.Equal(t, want, got, "Seek(%q)/Next sequence mismatch", target)
					}
				})
			}
		})
	}
}

func TestCursorOnDiskDigest_MatchesCursorOnDisk(t *testing.T) {
	ctx := context.Background()

	for _, mode := range digestCursorModes {
		t.Run(mode.name, func(t *testing.T) {
			b := newReusableTestBucket(t, ctx, append(append([]BucketOption{}, mode.opts...), WithSecondaryIndices(1))...)
			defer b.Shutdown(ctx)
			buildDigestCursorBucket(t, b, 1)

			for _, prefix := range digestCursorPrefixes {
				t.Run(fmt.Sprintf("prefix=%d", prefix), func(t *testing.T) {
					want := truncateDiskValues(drainDigestKVs(b.CursorOnDisk()), prefix, nil)
					require.NotEmpty(t, want)
					require.Equal(t, want, drainDigestKVs(b.CursorOnDiskDigest(prefix)),
						"First/Next sequence mismatch")

					for _, target := range digestSeekTargets {
						want := truncateDiskValues(drainDigestKVsFrom(b.CursorOnDisk(), []byte(target)), prefix, nil)
						got := drainDigestKVsFrom(b.CursorOnDiskDigest(prefix), []byte(target))
						require.Equal(t, want, got, "Seek(%q)/Next sequence mismatch", target)
					}
				})
			}
		})
	}
}
