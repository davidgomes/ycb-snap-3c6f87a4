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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/lsmkv"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func TestParseReplaceNodeDigest_OffsetAndPrefixParity(t *testing.T) {
	nodes := []segmentReplaceNode{
		{
			primaryKey:          []byte("a"),
			value:               bytes.Repeat([]byte{0x11}, 10),
			secondaryIndexCount: 1,
			secondaryKeys:       [][]byte{[]byte("sa")},
		},
		{
			primaryKey:          []byte("bb"),
			value:               bytes.Repeat([]byte{0x22}, 1000),
			secondaryIndexCount: 1,
			secondaryKeys:       [][]byte{nil},
		},
		{
			tombstone:           true,
			primaryKey:          []byte("ccc"),
			value:               bytes.Repeat([]byte{0x33}, 42),
			secondaryIndexCount: 1,
			secondaryKeys:       [][]byte{[]byte("sc")},
		},
		{
			primaryKey:          []byte("d"),
			value:               nil,
			secondaryIndexCount: 1,
			secondaryKeys:       [][]byte{[]byte{}},
		},
	}

	var buf bytes.Buffer
	var nodeLens []int
	for i := range nodes {
		before := buf.Len()
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
		nodeLens = append(nodeLens, buf.Len()-before)
	}
	raw := buf.Bytes()

	prefix := 42
	for _, prefixLen := range []int{0, prefix, 7} {
		t.Run("pread", func(t *testing.T) {
			assertDigestParse(t, raw, nodes, nodeLens, prefixLen, true)
		})
		t.Run("mmap", func(t *testing.T) {
			assertDigestParse(t, raw, nodes, nodeLens, prefixLen, false)
		})
	}
}

func assertDigestParse(t *testing.T, raw []byte, nodes []segmentReplaceNode, nodeLens []int, prefixLen int, pread bool) {
	t.Helper()
	var preadOut segmentReplaceNode
	var fullOut segmentReplaceNode
	off := 0
	for i, node := range nodes {
		chunk := raw[off:]
		var errDigest, errFull error
		if pread {
			errDigest = ParseReplaceNodeDigestIntoPread(bytes.NewReader(chunk), node.secondaryIndexCount, prefixLen, &preadOut)
			errFull = ParseReplaceNodeIntoPread(bytes.NewReader(chunk), node.secondaryIndexCount, &fullOut)
		} else {
			br := byteops.NewReadWriter(chunk)
			errDigest = ParseReplaceNodeDigestIntoMMAP(&br, node.secondaryIndexCount, prefixLen, &preadOut)
			br2 := byteops.NewReadWriter(chunk)
			errFull = ParseReplaceNodeIntoMMAP(&br2, node.secondaryIndexCount, &fullOut)
		}
		require.NoError(t, errDigest)
		require.NoError(t, errFull)
		require.Equal(t, fullOut.offset, preadOut.offset)
		require.Equal(t, nodeLens[i], preadOut.offset)
		require.Equal(t, node.tombstone, preadOut.tombstone)
		require.Equal(t, node.primaryKey, preadOut.primaryKey)
		want := node.value
		if prefixLen > 0 && len(want) > prefixLen {
			want = want[:prefixLen]
		}
		require.True(t, bytes.Equal(want, preadOut.value))
		if prefixLen <= 0 || len(node.value) <= prefixLen {
			require.True(t, bytes.Equal(fullOut.value, preadOut.value))
		}
		if node.secondaryIndexCount > 0 {
			require.True(t, bytes.Equal(node.secondaryKeys[0], preadOut.secondaryKeys[0]))
			require.True(t, bytes.Equal(fullOut.secondaryKeys[0], preadOut.secondaryKeys[0]))
		}
		// Reused buffer must be able to grow again on the next larger keep.
		off += nodeLens[i]
	}
}

func TestReplaceCursorDigestReusable_MmapAndPread(t *testing.T) {
	nodes := []segmentReplaceNode{
		{primaryKey: []byte("k0"), value: bytes.Repeat([]byte{1}, 8)},
		{primaryKey: []byte("k1"), value: bytes.Repeat([]byte{2}, 500)},
		{primaryKey: []byte("k2"), value: bytes.Repeat([]byte{3}, 20)},
		{tombstone: true, primaryKey: []byte("k3"), value: bytes.Repeat([]byte{4}, 80)},
	}
	var buf bytes.Buffer
	for i := range nodes {
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
	}
	raw := append([]byte(nil), buf.Bytes()...)

	prefix := 16
	check := func(t *testing.T, cur *segmentCursorReplaceReusable) {
		t.Helper()
		n, err := cur.first()
		var caps []int
		for i := 0; i < len(nodes); i++ {
			require.NotNil(t, n)
			if nodes[i].tombstone {
				require.ErrorIs(t, err, lsmkv.Deleted)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, nodes[i].primaryKey, append([]byte(nil), n.primaryKey...))
			want := nodes[i].value
			if len(want) > prefix {
				want = want[:prefix]
			}
			require.Equal(t, want, append([]byte(nil), n.value...))
			caps = append(caps, cap(n.value))
			if i+1 < len(nodes) {
				n, err = cur.next()
			}
		}
		_, err = cur.next()
		require.ErrorIs(t, err, lsmkv.NotFound)
		// small, then large prefix, then small again: capacity stays at the prefix.
		require.GreaterOrEqual(t, caps[1], prefix)
		require.Equal(t, prefix, caps[2])
		require.GreaterOrEqual(t, caps[2], caps[0])
	}

	t.Run("mmap", func(t *testing.T) {
		seg := &segment{
			readFromMemory: true,
			contents:       raw,
			dataEndPos:     uint64(len(raw)),
		}
		check(t, seg.newReplaceCursorDigestReusable(prefix))
	})

	t.Run("pread", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "seg")
		require.NoError(t, os.WriteFile(path, raw, 0o644))
		f, err := os.Open(path)
		require.NoError(t, err)
		t.Cleanup(func() { f.Close() })
		seg := &segment{
			contents:    raw,
			dataEndPos:  uint64(len(raw)),
			contentFile: f,
		}
		check(t, seg.newReplaceCursorDigestReusable(prefix))
	})

	t.Run("prefix zero matches full", func(t *testing.T) {
		seg := &segment{
			readFromMemory: true,
			contents:       raw,
			dataEndPos:     uint64(len(raw)),
		}
		full := seg.newReplaceCursorReusable()
		dig := seg.newReplaceCursorDigestReusable(0)
		fn, fe := full.first()
		dn, de := dig.first()
		for fn != nil || dn != nil {
			require.Equal(t, fe, de)
			require.Equal(t, fn.primaryKey, dn.primaryKey)
			require.Equal(t, fn.value, dn.value)
			require.Equal(t, fn.offset, dn.offset)
			require.Equal(t, fn.tombstone, dn.tombstone)
			fn, fe = full.next()
			dn, de = dig.next()
		}
	})
}
