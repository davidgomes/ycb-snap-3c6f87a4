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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func Test_SerializeAndParseCollectionNode(t *testing.T) {
	before := segmentCollectionNode{
		primaryKey: []byte("this-is-my-primary-key"),
		values: []value{{
			value: []byte("the-first-value"),
		}, {
			value:     []byte("the-second-value-with-a-tombstone"),
			tombstone: true,
		}},
	}

	buf := &bytes.Buffer{}
	_, err := before.KeyIndexAndWriteTo(buf)
	require.Nil(t, err)
	encoded := buf.Bytes()

	expected := segmentCollectionNode{
		primaryKey: []byte("this-is-my-primary-key"),
		values: []value{{
			value: []byte("the-first-value"),
		}, {
			value:     []byte("the-second-value-with-a-tombstone"),
			tombstone: true,
		}},
		offset: buf.Len(),
	}

	t.Run("parse using the _regular_ way", func(t *testing.T) {
		after, err := ParseCollectionNode(bytes.NewReader(encoded))
		assert.Nil(t, err)
		assert.Equal(t, expected, after)
	})

	t.Run("parse using the reusable way", func(t *testing.T) {
		var node segmentCollectionNode
		buf := [9]byte{}
		err := ParseCollectionNodeInto(bytes.NewReader(encoded), &node, buf[:])
		assert.Nil(t, err)
		assert.Equal(t, expected, node)
	})
}

type replaceDigestFixture struct {
	tombstone bool
	value     []byte
	key       []byte
	secondary [][]byte
}

func TestParseReplaceNodeDigest_PrefixOffsetAndBufferReuse(t *testing.T) {
	nodes := []replaceDigestFixture{
		{value: bytes.Repeat([]byte{0x11}, 8), key: []byte("k-short"), secondary: [][]byte{[]byte("aa"), {}}},
		{value: bytes.Repeat([]byte{0x22}, 10_000), key: []byte("k-large-value"), secondary: [][]byte{bytes.Repeat([]byte{0xAB}, 80), []byte("b")}},
		{tombstone: true, value: bytes.Repeat([]byte{0x33}, 200), key: []byte("k-tomb"), secondary: [][]byte{[]byte("c"), []byte("cd")}},
		{value: bytes.Repeat([]byte{0x44}, 42), key: []byte("k"), secondary: [][]byte{[]byte("d"), []byte("de")}},
		{value: bytes.Repeat([]byte{0x55}, 7), key: []byte("k-tiny"), secondary: [][]byte{[]byte("e"), {}}},
		{value: nil, key: []byte("k-empty"), secondary: [][]byte{{}, []byte("f")}},
	}

	var concat []byte
	encoded := make([][]byte, len(nodes))
	for i, n := range nodes {
		encoded[i] = encodeReplaceNode(t, n.tombstone, n.value, n.key, n.secondary)
		concat = append(concat, encoded[i]...)
	}

	const prefix = 42
	secondaryCount := uint16(2)

	t.Run("pread", func(t *testing.T) {
		assertDigestNodesMatchFull(t, concat, encoded, nodes, secondaryCount, prefix, false)
	})
	t.Run("pread bufio", func(t *testing.T) {
		// Small buffer so a value spans many refills; discard must stay in sync.
		br := bufio.NewReaderSize(bytes.NewReader(concat), 8)
		var reused segmentReplaceNode
		for i := range nodes {
			var full segmentReplaceNode
			require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(encoded[i]), secondaryCount, &full))
			require.NoError(t, ParseReplaceNodeDigestIntoPread(br, secondaryCount, prefix, &reused))
			assertDigestNode(t, nodes[i], &full, &reused, prefix)
		}
		_, err := br.ReadByte()
		require.Error(t, err)
	})
	t.Run("mmap", func(t *testing.T) {
		assertDigestNodesMatchFull(t, concat, encoded, nodes, secondaryCount, prefix, true)
	})
	t.Run("prefix zero and negative match full value", func(t *testing.T) {
		for _, n := range []int{0, -1} {
			var pread, mmap segmentReplaceNode
			require.NoError(t, ParseReplaceNodeDigestIntoPread(bytes.NewReader(encoded[1]), secondaryCount, n, &pread))
			rw := byteops.NewReadWriter(encoded[1])
			require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, secondaryCount, n, &mmap))
			require.Equal(t, nodes[1].value, pread.value)
			require.Equal(t, nodes[1].value, mmap.value)
			require.Equal(t, len(encoded[1]), pread.offset)
			require.Equal(t, len(encoded[1]), mmap.offset)
			require.GreaterOrEqual(t, cap(pread.value), len(nodes[1].value))
		}
	})
}

func assertDigestNodesMatchFull(t *testing.T, concat []byte, encoded [][]byte, nodes []replaceDigestFixture, secondaryCount uint16, prefix int, mmap bool,
) {
	t.Helper()
	var reused segmentReplaceNode
	offset := 0
	for i := range nodes {
		var full segmentReplaceNode
		require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(encoded[i]), secondaryCount, &full))

		if mmap {
			rw := byteops.NewReadWriter(concat[offset:])
			require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, secondaryCount, prefix, &reused))
			require.Equal(t, full.offset, int(rw.Position))
		} else {
			r := bytes.NewReader(concat[offset:])
			require.NoError(t, ParseReplaceNodeDigestIntoPread(r, secondaryCount, prefix, &reused))
			require.Equal(t, len(concat)-offset-full.offset, r.Len())
		}
		assertDigestNode(t, nodes[i], &full, &reused, prefix)
		// Retained bytes are a copy: mutating them must not touch the segment.
		if len(reused.value) > 0 {
			reused.value[0] ^= 0xFF
			require.Equal(t, nodes[i].value[0], concat[offset+9])
			reused.value[0] ^= 0xFF
		}
		// A value longer than the prefix must not grow the reusable buffer to the full payload.
		if len(nodes[i].value) > prefix {
			require.LessOrEqual(t, cap(reused.value), prefix)
		}
		offset += full.offset
	}
	require.Equal(t, len(concat), offset)
}

func assertDigestNode(t *testing.T, in replaceDigestFixture, full, digest *segmentReplaceNode, prefix int,
) {
	t.Helper()
	require.Equal(t, full.offset, digest.offset)
	require.Equal(t, full.tombstone, digest.tombstone)
	require.Equal(t, in.tombstone, digest.tombstone)
	require.Equal(t, in.key, digest.primaryKey)
	require.Equal(t, in.key, full.primaryKey)
	keep := prefix
	if keep > len(in.value) {
		keep = len(in.value)
	}
	require.True(t, bytes.Equal(in.value[:keep], digest.value), "prefix mismatch for key %q", in.key)
	require.True(t, bytes.Equal(in.value, full.value))
	require.Len(t, digest.secondaryKeys, len(in.secondary))
	for i := range in.secondary {
		require.True(t, bytes.Equal(in.secondary[i], digest.secondaryKeys[i]), "secondary key %d mismatch for %q", i, in.key)
	}
}

func encodeReplaceNode(t *testing.T, tombstone bool, value, key []byte, secondary [][]byte) []byte {
	t.Helper()
	node := segmentReplaceNode{
		tombstone:           tombstone,
		value:               value,
		primaryKey:          key,
		secondaryIndexCount: uint16(len(secondary)),
		secondaryKeys:       secondary,
	}
	buf := &bytes.Buffer{}
	_, err := node.KeyIndexAndWriteTo(buf)
	require.NoError(t, err)
	return buf.Bytes()
}
