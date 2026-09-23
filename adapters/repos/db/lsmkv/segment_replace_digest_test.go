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
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/lsmkv"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func TestParseReplaceNodeDigest_OffsetParityAndBufferReuse(t *testing.T) {
	const secondary = uint16(2)
	nodes := digestTestNodes()
	blob := encodeReplaceNodes(t, nodes)

	prefixes := []int{0, 1, 41, 42, 100, 50_000}
	for _, prefix := range prefixes {
		t.Run(fmt.Sprintf("prefix_%d", prefix), func(t *testing.T) {
			t.Run("pread_bytes", func(t *testing.T) {
				assertDigestPreadParity(t, blob, secondary, prefix, false)
			})
			t.Run("pread_bufio", func(t *testing.T) {
				assertDigestPreadParity(t, blob, secondary, prefix, true)
			})
			t.Run("mmap", func(t *testing.T) {
				assertDigestMmapParity(t, blob, secondary, prefix)
			})
		})
	}
}

func TestReplaceCursorDigest_SequentialMatchesFull(t *testing.T) {
	const secondary = uint16(2)
	nodes := digestTestNodes()
	blob := encodeReplaceNodes(t, nodes)

	for _, mmap := range []bool{true, false} {
		mode := "pread"
		if mmap {
			mode = "mmap"
		}
		t.Run(mode, func(t *testing.T) {
			seg := digestTestSegment(t, blob, mmap, secondary)

			full := walkReplaceReusable(t, seg.newReplaceCursorReusable())
			zero := walkReplaceReusable(t, seg.newReplaceCursorDigestReusable(0))
			requireWalkEqual(t, full, zero)

			for _, prefix := range []int{1, 42, 100, 50_000} {
				digest := walkReplaceReusable(t, seg.newReplaceCursorDigestReusable(prefix))
				require.Len(t, digest, len(full))
				for i := range full {
					require.Equal(t, full[i].key, digest[i].key, "key %d", i)
					require.Equal(t, full[i].offset, digest[i].offset, "offset %d", i)
					require.Equal(t, full[i].deleted, digest[i].deleted, "deleted %d", i)
					keep := len(full[i].value)
					if prefix < keep {
						keep = prefix
					}
					require.Equal(t, full[i].value[:keep], digest[i].value, "value %d", i)
					if len(full[i].value) > prefix {
						require.LessOrEqual(t, digest[i].valueCap, prefix,
							"value buffer grew to the discarded tail at node %d", i)
					}
				}
			}

			// first() must rewind; a prior next() must not stick.
			cur := seg.newReplaceCursorDigestReusable(42)
			first, err := cur.first()
			require.True(t, err == nil || errors.Is(err, lsmkv.Deleted))
			firstKey := append([]byte(nil), first.primaryKey...)
			_, err = cur.next()
			require.True(t, err == nil || errors.Is(err, lsmkv.Deleted) || errors.Is(err, lsmkv.NotFound))
			again, err := cur.first()
			require.True(t, err == nil || errors.Is(err, lsmkv.Deleted))
			require.Equal(t, firstKey, append([]byte(nil), again.primaryKey...))
		})
	}
}

func TestReplaceCursorDigest_EmptySegment(t *testing.T) {
	for _, mmap := range []bool{true, false} {
		seg := digestTestSegment(t, nil, mmap, 0)
		cur := seg.newReplaceCursorDigestReusable(42)
		_, err := cur.first()
		require.ErrorIs(t, err, lsmkv.NotFound)
	}
}

func assertDigestPreadParity(t *testing.T, blob []byte, secondary uint16, prefix int, useBufio bool) {
	t.Helper()
	var digest segmentReplaceNode
	offset := 0
	for offset < len(blob) {
		var full segmentReplaceNode
		require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(blob[offset:]), secondary, &full))

		var r interface{ Read([]byte) (int, error) }
		plain := bytes.NewReader(blob[offset:])
		if useBufio {
			r = bufio.NewReader(plain)
		} else {
			r = plain
		}
		require.NoError(t, ParseReplaceNodeDigestIntoPread(r, secondary, &digest, prefix))
		assertNodePrefixParity(t, &full, &digest, prefix)
		offset += full.offset
	}
	require.Equal(t, len(blob), offset)
}

func assertDigestMmapParity(t *testing.T, blob []byte, secondary uint16, prefix int) {
	t.Helper()
	var digest segmentReplaceNode
	offset := 0
	for offset < len(blob) {
		fullRW := byteops.NewReadWriter(blob[offset:])
		var full segmentReplaceNode
		require.NoError(t, ParseReplaceNodeIntoMMAP(&fullRW, secondary, &full))

		digRW := byteops.NewReadWriter(blob[offset:])
		require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&digRW, secondary, &digest, prefix))
		assertNodePrefixParity(t, &full, &digest, prefix)
		offset += full.offset
	}
	require.Equal(t, len(blob), offset)
}

func assertNodePrefixParity(t *testing.T, full, digest *segmentReplaceNode, prefix int) {
	t.Helper()
	require.Equal(t, full.offset, digest.offset)
	require.Equal(t, full.tombstone, digest.tombstone)
	require.True(t, bytes.Equal(full.primaryKey, digest.primaryKey), "primary key")
	require.Len(t, digest.secondaryKeys, len(full.secondaryKeys))
	for i := range full.secondaryKeys {
		require.True(t, bytes.Equal(full.secondaryKeys[i], digest.secondaryKeys[i]), "secondary %d", i)
	}
	keep := len(full.value)
	if prefix > 0 && prefix < keep {
		keep = prefix
	}
	require.Equal(t, keep, len(digest.value))
	require.True(t, bytes.Equal(full.value[:keep], digest.value))
	if prefix > 0 && len(full.value) > prefix {
		require.LessOrEqual(t, cap(digest.value), prefix)
	}
}

type walkedReplaceNode struct {
	key      []byte
	value    []byte
	offset   int
	deleted  bool
	valueCap int
}

func walkReplaceReusable(t *testing.T, c *segmentCursorReplaceReusable) []walkedReplaceNode {
	t.Helper()
	var out []walkedReplaceNode
	n, err := c.first()
	for {
		if errors.Is(err, lsmkv.NotFound) {
			return out
		}
		require.Truef(t, err == nil || errors.Is(err, lsmkv.Deleted), "unexpected error: %v", err)
		out = append(out, walkedReplaceNode{
			key:      append([]byte(nil), n.primaryKey...),
			value:    append([]byte(nil), n.value...),
			offset:   n.offset,
			deleted:  errors.Is(err, lsmkv.Deleted),
			valueCap: cap(n.value),
		})
		n, err = c.next()
	}
}

func requireWalkEqual(t *testing.T, want, got []walkedReplaceNode) {
	t.Helper()
	require.Len(t, got, len(want))
	for i := range want {
		require.Equal(t, want[i].key, got[i].key, "key %d", i)
		require.True(t, bytes.Equal(want[i].value, got[i].value), "value %d", i)
		require.Equal(t, want[i].offset, got[i].offset, "offset %d", i)
		require.Equal(t, want[i].deleted, got[i].deleted, "deleted %d", i)
	}
}

func digestTestNodes() []segmentReplaceNode {
	sizes := []int{10, 200_000, 3, 42, 41, 100, 0, 4096, 7}
	nodes := make([]segmentReplaceNode, len(sizes))
	for i, sz := range sizes {
		val := make([]byte, sz)
		for j := range val {
			val[j] = byte(i*31 + j)
		}
		keyLen := 1 + (i*13)%60
		key := bytes.Repeat([]byte{byte('a' + i)}, keyLen)
		var sec1 []byte
		if i%2 == 0 {
			sec1 = []byte{}
		} else {
			sec1 = []byte(fmt.Sprintf("sec2-%03d-xxxxxxxx", i))
		}
		nodes[i] = segmentReplaceNode{
			tombstone:           i == 4 || i == 7,
			value:               val,
			primaryKey:          key,
			secondaryIndexCount: 2,
			secondaryKeys: [][]byte{
				[]byte(fmt.Sprintf("sec-%03d", i)),
				sec1,
			},
		}
	}
	return nodes
}

func encodeReplaceNodes(t *testing.T, nodes []segmentReplaceNode) []byte {
	t.Helper()
	var buf bytes.Buffer
	for i := range nodes {
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
	}
	return buf.Bytes()
}

func digestTestSegment(t *testing.T, blob []byte, mmap bool, secondary uint16) *segment {
	t.Helper()
	seg := &segment{
		secondaryIndexCount: secondary,
		dataStartPos:        0,
		dataEndPos:          uint64(len(blob)),
		readFromMemory:      mmap,
	}
	if mmap {
		seg.contents = blob
		return seg
	}
	path := filepath.Join(t.TempDir(), "segment.bin")
	require.NoError(t, os.WriteFile(path, blob, 0o644))
	f, err := os.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = f.Close() })
	seg.contentFile = f
	return seg
}
