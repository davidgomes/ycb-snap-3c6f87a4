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

package lsmkv

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sirupsen/logrus/hooks/test"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/cyclemanager"
	"github.com/weaviate/weaviate/entities/lsmkv"
)

func TestCursorReplaceDigest_DiskAndMemtable(t *testing.T) {
	ctx := context.Background()
	const prefix = 32

	modes := []struct {
		name        string
		readFromMem bool
		opts        []BucketOption
	}{
		{name: "mmap", readFromMem: true},
		{name: "pread", readFromMem: false, opts: []BucketOption{WithPread(true), WithMinMMapSize(0)}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			b := newDigestTestBucket(t, ctx, mode.opts...)

			for i, n := range []int{8, 32, 31, 33, 10_000, 100, 1, 64, 32, 5_000} {
				key := fmt.Sprintf("k-%03d", i)
				putDigestKV(t, b, []byte(key), bytes.Repeat([]byte{byte(i + 1)}, n))
			}
			require.NoError(t, b.FlushAndSwitch())

			// Second segment: an update, a tombstone, and keys the first segment lacks.
			putDigestKV(t, b, []byte("k-003"), bytes.Repeat([]byte{0xAB}, 20_000))
			require.NoError(t, b.Delete([]byte("k-001"), WithSecondaryKey(0, []byte("s-k-001"))))
			putDigestKV(t, b, []byte("k-010"), bytes.Repeat([]byte{0x10}, 7))
			putDigestKV(t, b, []byte("k-011"), bytes.Repeat([]byte{0x11}, 80))
			require.NoError(t, b.FlushAndSwitch())

			assertSegmentDigestCap(t, b, prefix, mode.readFromMem)

			// Memtable wins over disk for these keys and must keep the full value.
			memKeys := map[string]bool{}
			memValue := bytes.Repeat([]byte{0xEE}, 9_000)
			putDigestKV(t, b, []byte("k-004"), memValue)
			memKeys["k-004"] = true
			putDigestKV(t, b, []byte("k-020"), bytes.Repeat([]byte{0x20}, 4_000))
			memKeys["k-020"] = true
			require.NoError(t, b.Delete([]byte("k-005"), WithSecondaryKey(0, []byte("s-k-005"))))

			full := collectCursor(b.Cursor())
			digest := collectCursor(b.CursorReplaceDigestReusable(prefix))
			require.NotEmpty(t, full)
			assertDigestView(t, full, digest, prefix, memKeys)

			fullSeek := collectSeek(b.Cursor(), []byte("k-003"))
			digestSeek := collectSeek(b.CursorReplaceDigestReusable(prefix), []byte("k-003"))
			assertDigestView(t, fullSeek, digestSeek, prefix, memKeys)

			// N=0 matches the full-value cursor, including memtable bytes.
			assertSameCursorView(t, full, collectCursor(b.CursorReplaceDigestReusable(0)))
			assertSameCursorView(t, full, collectCursor(b.CursorReplaceReusable()))

			diskFull := collectCursor(b.CursorOnDisk())
			diskDigest := collectCursor(b.CursorOnDiskDigest(prefix))
			assertDigestView(t, diskFull, diskDigest, prefix, nil)
			assertSameCursorView(t, diskFull, collectCursor(b.CursorOnDiskDigest(0)))

			// Memtable-only keys are absent on disk; disk still has the pre-delete value.
			requireCursorHas(t, diskFull, "k-005")
			requireCursorMissing(t, full, "k-005")
			requireCursorMissing(t, diskFull, "k-020")
			requireCursorHas(t, full, "k-020")
		})
	}
}

type cursorKV struct {
	key string
	val []byte
}

func collectCursor(c *CursorReplace) []cursorKV {
	defer c.Close()
	var out []cursorKV
	for k, v := c.First(); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{key: string(k), val: append([]byte(nil), v...)})
	}
	return out
}

func collectSeek(c *CursorReplace, target []byte) []cursorKV {
	defer c.Close()
	var out []cursorKV
	for k, v := c.Seek(target); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{key: string(k), val: append([]byte(nil), v...)})
	}
	return out
}

func assertDigestView(t *testing.T, full, digest []cursorKV, prefix int, memKeys map[string]bool) {
	t.Helper()
	require.Equal(t, len(full), len(digest))
	for i := range full {
		require.Equal(t, full[i].key, digest[i].key)
		fv, dv := full[i].val, digest[i].val
		if prefix <= 0 || memKeys[full[i].key] || len(fv) <= prefix {
			require.Equalf(t, fv, dv, "key %s", full[i].key)
			continue
		}
		require.Equalf(t, fv[:prefix], dv, "key %s", full[i].key)
		require.Lenf(t, dv, prefix, "key %s", full[i].key)
	}
}

func assertSameCursorView(t *testing.T, want, got []cursorKV) {
	t.Helper()
	require.Equal(t, len(want), len(got))
	for i := range want {
		require.Equal(t, want[i].key, got[i].key)
		require.Equal(t, want[i].val, got[i].val)
	}
}

func requireCursorHas(t *testing.T, rows []cursorKV, key string) {
	t.Helper()
	for _, row := range rows {
		if row.key == key {
			return
		}
	}
	t.Fatalf("missing key %s", key)
}

func requireCursorMissing(t *testing.T, rows []cursorKV, key string) {
	t.Helper()
	for _, row := range rows {
		if row.key == key {
			t.Fatalf("unexpected key %s", key)
		}
	}
}

func assertSegmentDigestCap(t *testing.T, b *Bucket, prefix int, readFromMem bool) {
	t.Helper()
	require.NotEmpty(t, b.disk.segments)
	for _, seg := range b.disk.segments {
		raw := segmentFromAny(t, seg)
		require.Equal(t, readFromMem, raw.readFromMemory)

		c := seg.newReplaceCursorDigestReusable(prefix)
		maxCap := 0
		node, err := c.first()
		for {
			if err != nil && !errors.Is(err, lsmkv.Deleted) {
				require.ErrorIs(t, err, lsmkv.NotFound)
				break
			}
			if node == nil {
				break
			}
			require.LessOrEqual(t, len(node.value), prefix)
			if cap(node.value) > maxCap {
				maxCap = cap(node.value)
			}
			node, err = c.next()
		}
		require.Equal(t, prefix, maxCap)
	}
}

func segmentFromAny(t *testing.T, seg Segment) *segment {
	t.Helper()
	switch s := seg.(type) {
	case *segment:
		return s
	case *lazySegment:
		s.mustLoad()
		return s.segment
	default:
		t.Fatalf("unexpected segment type %T", seg)
		return nil
	}
}

func putDigestKV(t *testing.T, b *Bucket, key, val []byte) {
	t.Helper()
	sec := append([]byte("s-"), key...)
	require.NoError(t, b.Put(key, val, WithSecondaryKey(0, sec)))
}

func newDigestTestBucket(t *testing.T, ctx context.Context, opts ...BucketOption) *Bucket {
	t.Helper()
	logger, _ := test.NewNullLogger()
	dir := t.TempDir()
	o := append([]BucketOption{
		WithStrategy(StrategyReplace),
		WithSecondaryIndices(1),
	}, opts...)
	b, err := NewBucketCreator().NewBucket(ctx, dir, dir, logger, nil,
		cyclemanager.NewCallbackGroupNoop(), cyclemanager.NewCallbackGroupNoop(), o...)
	require.NoError(t, err)
	b.SetMemtableThreshold(1 << 30)
	t.Cleanup(func() {
		require.NoError(t, b.Shutdown(context.Background()))
	})
	return b
}
