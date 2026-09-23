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

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func TestParseReplaceNodeDigestInto_OffsetParity(t *testing.T) {
	nodes := []segmentReplaceNode{
		{value: bytes.Repeat([]byte{1}, 1000), primaryKey: []byte("k1"), secondaryIndexCount: 2, secondaryKeys: [][]byte{[]byte("s1"), nil}},
		{value: []byte{2, 3}, primaryKey: []byte("key-two"), secondaryIndexCount: 2, secondaryKeys: [][]byte{nil, []byte("sec")}},
		{tombstone: true, value: nil, primaryKey: []byte("k3"), secondaryIndexCount: 2, secondaryKeys: [][]byte{nil, nil}},
		{value: bytes.Repeat([]byte{4}, 70000), primaryKey: []byte("k4"), secondaryIndexCount: 2, secondaryKeys: [][]byte{[]byte("a"), []byte("b")}},
		{value: bytes.Repeat([]byte{5}, 42), primaryKey: []byte("k5"), secondaryIndexCount: 2, secondaryKeys: [][]byte{nil, nil}},
	}
	var buf bytes.Buffer
	for i := range nodes {
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
	}
	data := buf.Bytes()

	for _, prefix := range []int{0, 1, 42, 100000} {
		var fullP, digP, fullM, digM segmentReplaceNode
		offset := 0
		for i, n := range nodes {
			require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(data[offset:]), 2, &fullP))
			require.NoError(t, ParseReplaceNodeDigestIntoPread(bufio.NewReader(bytes.NewReader(data[offset:])), 2, prefix, &digP))
			rw := byteops.NewReadWriter(data[offset:])
			require.NoError(t, ParseReplaceNodeIntoMMAP(&rw, 2, &fullM))
			rw = byteops.NewReadWriter(data[offset:])
			require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, 2, prefix, &digM))

			want := n.value
			if prefix > 0 && len(want) > prefix {
				want = want[:prefix]
			}
			for _, got := range []*segmentReplaceNode{&digP, &digM} {
				require.Equal(t, fullP.offset, got.offset, "node %d prefix %d", i, prefix)
				require.Equal(t, n.tombstone, got.tombstone)
				require.Equal(t, n.primaryKey, got.primaryKey)
				require.Equal(t, len(want), len(got.value))
				require.True(t, bytes.Equal(want, got.value))
				for j := range n.secondaryKeys {
					require.True(t, bytes.Equal(n.secondaryKeys[j], got.secondaryKeys[j]))
				}
			}
			require.Equal(t, fullP.offset, fullM.offset)
			// plain io.Reader (no Discard) must also work
			var plain segmentReplaceNode
			require.NoError(t, ParseReplaceNodeDigestIntoPread(bytes.NewReader(data[offset:]), 2, prefix, &plain))
			require.Equal(t, fullP.offset, plain.offset)
			offset += fullP.offset
		}
		require.Equal(t, len(data), offset)
	}
}
