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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/cyclemanager"
	"github.com/weaviate/weaviate/entities/storobj"
)

type digestKV struct {
	key, value []byte
}

func digestValue(round, i, size int) []byte {
	v := make([]byte, size)
	for j := range v {
		v[j] = byte(round*31 + i + j)
	}
	return v
}

// setupDigestBucket builds three overlapping disk segments (with updates,
// deletes and resurrections across rounds) plus a non-empty active memtable.
// memtableKeys reports which keys are served from the memtable.
func setupDigestBucket(t *testing.T, ctx context.Context, pread bool) (*Bucket, map[string]bool) {
	t.Helper()
	dir := t.TempDir()
	b, err := NewBucketCreator().NewBucket(ctx, dir, dir, nullLogger(), nil,
		cyclemanager.NewCallbackGroupNoop(), cyclemanager.NewCallbackGroupNoop(),
		WithStrategy(StrategyReplace), WithPread(pread))
	require.NoError(t, err)
	b.SetMemtableThreshold(1e9)

	key := func(i int) []byte { return []byte(fmt.Sprintf("key-%04d", i)) }
	sizeFor := func(round, i int) int {
		switch (round + i) % 5 {
		case 0:
			return 0
		case 1:
			return storobj.MarshallerV1HeaderLen - 3
		case 2:
			return storobj.MarshallerV1HeaderLen
		case 3:
			return 8_000
		default:
			return 30_000
		}
	}

	for round := 0; round < 3; round++ {
		for i := round * 10; i < 60+round*10; i++ {
			require.NoError(t, b.Put(key(i), digestValue(round, i, sizeFor(round, i))))
		}
		for i := round; i < 80; i += 7 {
			require.NoError(t, b.Delete(key(i)))
		}
		require.NoError(t, b.FlushAndSwitch())
	}

	memtableKeys := map[string]bool{}
	for i := 5; i < 95; i += 9 {
		require.NoError(t, b.Put(key(i), digestValue(3, i, 12_000)))
		memtableKeys[string(key(i))] = true
	}
	require.NoError(t, b.Delete(key(14)))

	require.Len(t, b.disk.segments, 3)
	return b, memtableKeys
}

func drainDigestCursor(c *CursorReplace, start func() ([]byte, []byte)) []digestKV {
	var out []digestKV
	for k, v := start(); k != nil; k, v = c.Next() {
		out = append(out, digestKV{key: copySlice(k), value: copySlice(v)})
	}
	return out
}

func requireDigestMatchesFull(t *testing.T, full, digest []digestKV,
	valuePrefixLen int, memtableKeys map[string]bool,
) {
	t.Helper()
	require.NotEmpty(t, full)
	require.Len(t, digest, len(full))
	for i := range full {
		require.Equal(t, full[i].key, digest[i].key, "key at %d", i)
		want := full[i].value
		if valuePrefixLen > 0 && !memtableKeys[string(full[i].key)] && len(want) > valuePrefixLen {
			want = want[:valuePrefixLen]
		}
		require.True(t, bytes.Equal(want, digest[i].value),
			"value at %d (key %s): want len %d, got len %d", i, full[i].key, len(want), len(digest[i].value))
	}
}

func TestCursorReplaceDigest_MatchesFullCursor(t *testing.T) {
	ctx := context.Background()
	prefixLens := []int{0, 1, storobj.MarshallerV1HeaderLen, 100_000}
	seekKeys := [][]byte{nil, []byte("key-0000"), []byte("key-0014"), []byte("key-0033x"), []byte("key-0079"), []byte("zzz")}

	for _, pread := range []bool{false, true} {
		t.Run(fmt.Sprintf("pread=%v", pread), func(t *testing.T) {
			b, memtableKeys := setupDigestBucket(t, ctx, pread)
			defer b.Shutdown(ctx)

			for _, prefixLen := range prefixLens {
				t.Run(fmt.Sprintf("prefix=%d", prefixLen), func(t *testing.T) {
					t.Run("CursorReplaceDigestReusable/first", func(t *testing.T) {
						fc := b.CursorReplaceReusable()
						defer fc.Close()
						dc := b.CursorReplaceDigestReusable(prefixLen)
						defer dc.Close()
						requireDigestMatchesFull(t, drainDigestCursor(fc, fc.First), drainDigestCursor(dc, dc.First),
							prefixLen, memtableKeys)
					})

					for _, seek := range seekKeys {
						t.Run(fmt.Sprintf("CursorReplaceDigestReusable/seek=%s", seek), func(t *testing.T) {
							fc := b.CursorReplaceReusable()
							defer fc.Close()
							dc := b.CursorReplaceDigestReusable(prefixLen)
							defer dc.Close()
							full := drainDigestCursor(fc, func() ([]byte, []byte) { return fc.Seek(seek) })
							digest := drainDigestCursor(dc, func() ([]byte, []byte) { return dc.Seek(seek) })
							if len(full) == 0 {
								require.Empty(t, digest)
								return
							}
							requireDigestMatchesFull(t, full, digest, prefixLen, memtableKeys)
						})
					}

					t.Run("CursorReplaceDigestReusable/reseek", func(t *testing.T) {
						// repeated seeks on one cursor, as the async-replication
						// scan does across hashtree ranges
						fc := b.CursorReplaceReusable()
						defer fc.Close()
						dc := b.CursorReplaceDigestReusable(prefixLen)
						defer dc.Close()
						for _, seek := range [][]byte{[]byte("key-0050"), []byte("key-0003"), []byte("key-0071")} {
							var full, digest []digestKV
							fk, fv := fc.Seek(seek)
							dk, dv := dc.Seek(seek)
							for n := 0; n < 7 && fk != nil; n++ {
								full = append(full, digestKV{copySlice(fk), copySlice(fv)})
								digest = append(digest, digestKV{copySlice(dk), copySlice(dv)})
								fk, fv = fc.Next()
								dk, dv = dc.Next()
							}
							requireDigestMatchesFull(t, full, digest, prefixLen, memtableKeys)
						}
					})

					t.Run("CursorOnDiskDigest", func(t *testing.T) {
						fc := b.CursorOnDisk()
						defer fc.Close()
						dc := b.CursorOnDiskDigest(prefixLen)
						defer dc.Close()
						requireDigestMatchesFull(t, drainDigestCursor(fc, fc.First), drainDigestCursor(dc, dc.First),
							prefixLen, nil)
					})
				})
			}
		})
	}
}

// TestSegmentCursorReplaceDigestReusable_NodeSpanParity pins that digest mode
// reports the same full on-disk node span as the full-value cursor, so
// sequential next() stays aligned.
func TestSegmentCursorReplaceDigestReusable_NodeSpanParity(t *testing.T) {
	ctx := context.Background()
	for _, pread := range []bool{false, true} {
		t.Run(fmt.Sprintf("pread=%v", pread), func(t *testing.T) {
			b, _ := setupDigestBucket(t, ctx, pread)
			defer b.Shutdown(ctx)

			for segIdx, seg := range b.disk.segments {
				realSeg, ok := seg.(*segment)
				require.True(t, ok)
				require.Equal(t, !pread, realSeg.readFromMemory)

				full := realSeg.newReplaceCursorReusable()
				digest := realSeg.newReplaceCursorDigestReusable(storobj.MarshallerV1HeaderLen)

				fn, ferr := full.first()
				dn, derr := digest.first()
				for n := 0; fn != nil; n++ {
					require.NotNil(t, dn, "segment %d node %d", segIdx, n)
					require.Equal(t, ferr, derr, "segment %d node %d", segIdx, n)
					require.Equal(t, fn.offset, dn.offset, "segment %d node %d", segIdx, n)
					require.Equal(t, full.currOffset, digest.currOffset, "segment %d node %d", segIdx, n)
					require.Equal(t, fn.tombstone, dn.tombstone)
					require.Equal(t, fn.primaryKey, dn.primaryKey)
					want := fn.value
					if len(want) > storobj.MarshallerV1HeaderLen {
						want = want[:storobj.MarshallerV1HeaderLen]
					}
					require.True(t, bytes.Equal(want, dn.value))
					require.LessOrEqual(t, len(dn.value), storobj.MarshallerV1HeaderLen)

					fn, ferr = full.next()
					dn, derr = digest.next()
				}
				require.Nil(t, dn)
				require.Equal(t, ferr, derr)
			}
		})
	}
}
