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

// TestCursorReplaceDigestReusable_PrefixKeepsNodeSpan checks that digest
// cursors return only the requested value prefix while visiting the same keys
// as the full-value cursor, on both mmap and pread, including across value
// sizes, tombstones, segment merges, and memtable entries (which stay full).
func TestCursorReplaceDigestReusable_PrefixKeepsNodeSpan(t *testing.T) {
	ctx := context.Background()
	const prefix = 42

	modes := []struct {
		name string
		opts []BucketOption
	}{
		{"mmap", nil},
		{"pread", []BucketOption{WithPread(true), WithMinMMapSize(0)}},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			opts := append([]BucketOption{WithSecondaryIndices(1)}, mode.opts...)
			b := newReusableTestBucket(t, ctx, opts...)
			defer b.Shutdown(ctx)

			put := func(key string, n int, fill byte, sec string) {
				t.Helper()
				val := bytes.Repeat([]byte{fill}, n)
				require.NoError(t, b.Put([]byte(key), val, WithSecondaryKey(0, []byte(sec))))
			}

			put("a", 10, 0x11, "sa")
			put("b", 5000, 0x22, "sb-long-secondary")
			put("c", 80, 0x33, "sc")
			put("d", 4000, 0x44, "sd")
			put("e", 7, 0x55, "se")
			require.NoError(t, b.FlushAndSwitch())

			require.NoError(t, b.Delete([]byte("d"), WithSecondaryKey(0, []byte("sd"))))
			put("b", 100, 0x26, "sb2")
			put("f", prefix, 0x66, "sf")
			require.NoError(t, b.FlushAndSwitch())

			put("g", 200, 0x77, "sg")
			put("c", 300, 0x39, "sc2")

			full := drainCursorPairs(b.Cursor())
			zero := drainCursorPairs(b.CursorReplaceDigestReusable(0))
			require.Equal(t, full, zero, "prefix 0 must match the full-value cursor")

			digest := drainCursorPairs(b.CursorReplaceDigestReusable(prefix))
			require.Equal(t, []string{"a", "b", "c", "e", "f", "g"}, cursorKeys(digest))
			require.Equal(t, cursorKeys(full), cursorKeys(digest))

			memtableFull := map[string]bool{"c": true, "g": true}
			for i, pair := range digest {
				want := full[i].value
				if memtableFull[pair.key] {
					require.Equal(t, want, pair.value, "memtable value %q must stay full", pair.key)
					continue
				}
				keep := prefix
				if keep > len(want) {
					keep = len(want)
				}
				require.Equal(t, want[:keep], pair.value, "on-disk prefix for %q", pair.key)
			}

			wantSeek := drainSeekPairs(b.Cursor(), []byte("b"))
			gotSeek := drainSeekPairs(b.CursorReplaceDigestReusable(prefix), []byte("b"))
			require.Equal(t, cursorKeys(wantSeek), cursorKeys(gotSeek))
			require.Equal(t, []string{"b", "c", "e", "f", "g"}, cursorKeys(gotSeek))

			// Reusing one cursor across two full scans must not leak bytes between value sizes.
			cur := b.CursorReplaceDigestReusable(prefix)
			firstPass := collectCursorPairs(cur)
			curFirstAgain := collectCursorPairs(cur)
			cur.Close()
			require.Equal(t, digest, firstPass)
			require.Equal(t, firstPass, curFirstAgain)

			diskFull := drainCursorPairs(b.CursorOnDisk())
			diskDigest := drainCursorPairs(b.CursorOnDiskDigest(prefix))
			require.Equal(t, []string{"a", "b", "c", "e", "f"}, cursorKeys(diskFull))
			require.Equal(t, cursorKeys(diskFull), cursorKeys(diskDigest))
			for i, pair := range diskDigest {
				want := diskFull[i].value
				keep := prefix
				if keep > len(want) {
					keep = len(want)
				}
				require.Equal(t, want[:keep], pair.value, "on-disk digest prefix for %q", pair.key)
			}
			diskZero := drainCursorPairs(b.CursorOnDiskDigest(0))
			require.Equal(t, diskFull, diskZero)
		})
	}
}

type cursorKV struct {
	key   string
	value []byte
}

func cursorKeys(in []cursorKV) []string {
	out := make([]string, len(in))
	for i, kv := range in {
		out[i] = kv.key
	}
	return out
}

func drainCursorPairs(c *CursorReplace) []cursorKV {
	defer c.Close()
	return collectCursorPairs(c)
}

func drainSeekPairs(c *CursorReplace, target []byte) []cursorKV {
	defer c.Close()
	var out []cursorKV
	for k, v := c.Seek(target); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{key: string(k), value: append([]byte(nil), v...)})
	}
	return out
}

func collectCursorPairs(c *CursorReplace) []cursorKV {
	var out []cursorKV
	for k, v := c.First(); k != nil; k, v = c.Next() {
		out = append(out, cursorKV{key: string(k), value: append([]byte(nil), v...)})
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
