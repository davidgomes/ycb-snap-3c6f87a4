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

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/cyclemanager"
)

// TestCursorReplaceReusable_MatchesCursor proves that CursorReplaceReusable
// yields the exact same merged view (First/Next and Seek/Next) as the default
// Cursor across multiple disk segments, a live memtable, overlaps and
// tombstones, in both the mmap (readFromMemory) and pread code paths.
func TestCursorReplaceReusable_MatchesCursor(t *testing.T) {
	ctx := context.Background()

	modes := []struct {
		name        string
		readFromMem bool
		opts        []BucketOption
	}{
		{"mmap", true, nil},
		{"pread", false, []BucketOption{WithPread(true), WithMinMMapSize(0)}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			b := newReusableTestBucket(t, ctx, mode.opts...)
			defer b.Shutdown(ctx)

			for i := 0; i < 50; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)),
					[]byte(fmt.Sprintf("v1-%03d", i))))
			}
			require.NoError(t, b.FlushAndSwitch())

			for i := 25; i < 75; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)),
					[]byte(fmt.Sprintf("v2-%03d", i))))
			}
			for i := 0; i < 50; i += 5 {
				require.NoError(t, b.Delete([]byte(fmt.Sprintf("key-%03d", i))))
			}
			require.NoError(t, b.FlushAndSwitch())

			for i := 60; i < 90; i++ {
				require.NoError(t, b.Put([]byte(fmt.Sprintf("key-%03d", i)),
					[]byte(fmt.Sprintf("v3-%03d", i))))
			}
			for i := 31; i < 40; i += 3 {
				require.NoError(t, b.Delete([]byte(fmt.Sprintf("key-%03d", i))))
			}

			for _, seg := range b.disk.segments {
				if s, ok := seg.(*segment); ok {
					require.Equal(t, mode.readFromMem, s.readFromMemory,
						"segment readFromMemory must match the configured access mode")
				}
			}

			want := drainCursor(b.Cursor())
			got := drainCursor(b.CursorReplaceReusable())
			require.Equal(t, want, got, "First/Next sequence mismatch")
			require.NotEmpty(t, got)

			for _, target := range []string{
				"", "key-000", "key-001", "key-033", "key-050", "key-074", "key-089", "key-999",
			} {
				w := drainSeek(b.Cursor(), []byte(target))
				g := drainSeek(b.CursorReplaceReusable(), []byte(target))
				require.Equal(t, w, g, "Seek(%q)/Next sequence mismatch", target)
			}
		})
	}
}

func drainCursor(c *CursorReplace) []string {
	defer c.Close()
	var out []string
	for k, v := c.First(); k != nil; k, v = c.Next() {
		out = append(out, string(k)+"="+string(v))
	}
	return out
}

func drainSeek(c *CursorReplace, target []byte) []string {
	defer c.Close()
	var out []string
	for k, v := c.Seek(target); k != nil; k, v = c.Next() {
		out = append(out, string(k)+"="+string(v))
	}
	return out
}

// TestCursorReplaceDigest_OnDiskPrefixMemtableFull proves that digest cursors
// keep seek/merge order identical to the full-value cursors, return full
// memtable values, and return only a prefix of on-disk values. N=0 matches
// the full-value cursors on both mmap and pread.
func TestCursorReplaceDigest_OnDiskPrefixMemtableFull(t *testing.T) {
	ctx := context.Background()

	modes := []struct {
		name string
		opts []BucketOption
	}{
		{"mmap", nil},
		{"pread", []BucketOption{WithPread(true), WithMinMMapSize(0)}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			b := newReusableTestBucket(t, ctx, mode.opts...)
			defer b.Shutdown(ctx)

			const prefix = 42
			put := func(key string, n int) {
				val := make([]byte, n)
				for i := range val {
					val[i] = byte(len(key) + i)
				}
				require.NoError(t, b.Put([]byte(key), val))
			}

			put("key-000", 10)
			put("key-001", 8000)
			put("key-002", 3)
			put("key-003", 42)
			put("key-004", 100)
			put("key-005", 8000)
			require.NoError(t, b.Delete([]byte("key-002")))
			require.NoError(t, b.FlushAndSwitch())

			// Second on-disk segment: overwrite, delete, and a short value after a long one.
			put("key-001", 50)
			put("key-006", 9000)
			require.NoError(t, b.Delete([]byte("key-004")))
			require.NoError(t, b.FlushAndSwitch())

			mem := map[string]struct{}{}
			memPut := func(key string, n int) {
				val := make([]byte, n)
				for i := range val {
					val[i] = byte(200 + i)
				}
				require.NoError(t, b.Put([]byte(key), val))
				mem[key] = struct{}{}
			}
			memPut("key-005", 7000) // overwrites an on-disk value
			memPut("key-007", 20)
			require.NoError(t, b.Delete([]byte("key-000"))) // memtable tombstone hides disk value

			require.Equal(t, drainCursor(b.Cursor()), drainCursor(b.CursorReplaceDigestReusable(0)))
			require.Equal(t, drainCursor(b.CursorOnDisk()), drainCursor(b.CursorOnDiskDigest(0)))

			full := drainKVs(b.Cursor())
			got := drainKVs(b.CursorReplaceDigestReusable(prefix))
			require.Len(t, got, len(full))
			for i := range full {
				require.Equal(t, full[i].k, got[i].k)
				want := full[i].v
				if _, inMem := mem[string(full[i].k)]; !inMem && len(want) > prefix {
					want = want[:prefix]
				}
				require.Equal(t, want, got[i].v, "key %s", full[i].k)
			}

			diskFull := drainKVs(b.CursorOnDisk())
			diskGot := drainKVs(b.CursorOnDiskDigest(prefix))
			require.Len(t, diskGot, len(diskFull))
			require.NotEmpty(t, diskGot)
			for i := range diskFull {
				require.Equal(t, diskFull[i].k, diskGot[i].k)
				want := diskFull[i].v
				if len(want) > prefix {
					want = want[:prefix]
					require.Len(t, diskGot[i].v, prefix)
				}
				require.Equal(t, want, diskGot[i].v, "on-disk key %s", diskFull[i].k)
			}

			for _, target := range []string{"", "key-000", "key-001", "key-005", "key-006", "key-007", "key-999"} {
				w := drainSeekKVs(b.Cursor(), []byte(target))
				g := drainSeekKVs(b.CursorReplaceDigestReusable(prefix), []byte(target))
				require.Len(t, g, len(w), "Seek(%s)", target)
				for i := range w {
					require.Equal(t, w[i].k, g[i].k, "Seek(%s)", target)
					want := w[i].v
					if _, inMem := mem[string(w[i].k)]; !inMem && len(want) > prefix {
						want = want[:prefix]
					}
					require.Equal(t, want, g[i].v, "Seek(%s) key %s", target, w[i].k)
				}
			}
		})
	}
}

type cursorKV struct {
	k []byte
	v []byte
}

func drainKVs(c *CursorReplace) []cursorKV {
	defer c.Close()
	var out []cursorKV
	for k, v := c.First(); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{
			k: append([]byte(nil), k...),
			v: append([]byte(nil), v...),
		})
	}
	return out
}

func drainSeekKVs(c *CursorReplace, target []byte) []cursorKV {
	defer c.Close()
	var out []cursorKV
	for k, v := c.Seek(target); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{
			k: append([]byte(nil), k...),
			v: append([]byte(nil), v...),
		})
	}
	return out
}

func newReusableTestBucket(t *testing.T, ctx context.Context, opts ...BucketOption) *Bucket {
	t.Helper()
	dir := t.TempDir()
	o := append([]BucketOption{WithStrategy(StrategyReplace)}, opts...)
	b, err := NewBucketCreator().NewBucket(ctx, dir, dir, nullLogger(), nil,
		cyclemanager.NewCallbackGroupNoop(), cyclemanager.NewCallbackGroupNoop(), o...)
	require.NoError(t, err)
	b.SetMemtableThreshold(1e9)
	return b
}
