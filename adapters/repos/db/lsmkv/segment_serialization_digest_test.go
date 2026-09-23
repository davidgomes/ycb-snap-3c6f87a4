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
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func digestTestNode(valueLen int, tombstone bool, secondaryIndexCount uint16, seed byte) segmentReplaceNode {
	value := make([]byte, valueLen)
	for i := range value {
		value[i] = seed + byte(i)
	}
	n := segmentReplaceNode{
		tombstone:           tombstone,
		value:               value,
		primaryKey:          []byte(fmt.Sprintf("key-%03d", seed)),
		secondaryIndexCount: secondaryIndexCount,
		secondaryKeys:       make([][]byte, secondaryIndexCount),
	}
	for j := range n.secondaryKeys {
		if j%2 == 1 {
			continue // leave odd secondary keys empty
		}
		n.secondaryKeys[j] = []byte(fmt.Sprintf("sec-%d-%03d", j, seed))
	}
	return n
}

func serializeDigestTestNodes(t *testing.T, nodes []segmentReplaceNode) []byte {
	t.Helper()
	var buf bytes.Buffer
	for i := range nodes {
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
	}
	return buf.Bytes()
}

func expectedPrefix(value []byte, prefixLen int) []byte {
	if prefixLen <= 0 || len(value) <= prefixLen {
		return value
	}
	return value[:prefixLen]
}

func normalizeSecondaryKeys(keys [][]byte) [][]byte {
	out := make([][]byte, len(keys))
	for i, k := range keys {
		if len(k) > 0 {
			out[i] = k
		}
	}
	return out
}

// readerFactory builds a reader over data using a specific io.Reader flavor,
// so the pread digest parser is exercised with and without Discard support.
type readerFactory struct {
	name string
	make func(data []byte) io.Reader
}

var digestReaderFactories = []readerFactory{
	{
		name: "plain reader (no Discard)",
		make: func(data []byte) io.Reader { return struct{ io.Reader }{bytes.NewReader(data)} },
	},
	{
		name: "bufio reader",
		make: func(data []byte) io.Reader { return bufio.NewReader(bytes.NewReader(data)) },
	},
	{
		name: "preadSkipReader small buffer",
		make: func(data []byte) io.Reader {
			or := &offsetReader{ra: bytes.NewReader(data)}
			return &preadSkipReader{or: or, br: bufio.NewReaderSize(or, 16)}
		},
	},
	{
		name: "preadSkipReader default buffer",
		make: func(data []byte) io.Reader {
			or := &offsetReader{ra: bytes.NewReader(data)}
			return &preadSkipReader{or: or, br: bufio.NewReader(or)}
		},
	},
}

func TestParseReplaceNodeDigest_MatchesFullParser(t *testing.T) {
	valueLens := []int{0, 1, 41, 42, 43, 100, 4095, 4096, 4097, 10_000}
	prefixLens := []int{-1, 0, 1, 42, 64, 20_000}
	secondaryCounts := []uint16{0, 1, 3}

	for _, secCount := range secondaryCounts {
		for _, prefixLen := range prefixLens {
			t.Run(fmt.Sprintf("sec=%d/prefix=%d", secCount, prefixLen), func(t *testing.T) {
				// All value sizes back to back in one stream, including a
				// tombstone, so buffer reuse across shrinking/growing values and
				// sequential positioning are both exercised.
				nodes := make([]segmentReplaceNode, 0, len(valueLens)+1)
				for i, l := range valueLens {
					nodes = append(nodes, digestTestNode(l, false, secCount, byte(i)))
				}
				nodes = append(nodes, digestTestNode(0, true, secCount, 200))
				// big -> small -> big to catch stale reusable-buffer contents
				nodes = append(nodes, digestTestNode(10_000, false, secCount, 201))
				nodes = append(nodes, digestTestNode(5, false, secCount, 202))
				nodes = append(nodes, digestTestNode(10_000, false, secCount, 203))
				data := serializeDigestTestNodes(t, nodes)

				// Reference: full parsers.
				fullOffsets := make([]int, len(nodes))
				{
					var full segmentReplaceNode
					r := bufio.NewReader(bytes.NewReader(data))
					for i := range nodes {
						require.NoError(t, ParseReplaceNodeIntoPread(r, secCount, &full))
						fullOffsets[i] = full.offset
						require.True(t, bytes.Equal(nodes[i].value, full.value))
					}
				}

				for _, rf := range digestReaderFactories {
					t.Run("pread/"+rf.name, func(t *testing.T) {
						r := rf.make(data)
						var out segmentReplaceNode
						for i, want := range nodes {
							require.NoError(t, ParseReplaceNodeDigestIntoPread(r, secCount, prefixLen, &out), "node %d", i)
							assert.Equal(t, fullOffsets[i], out.offset, "offset node %d", i)
							assert.Equal(t, want.tombstone, out.tombstone, "tombstone node %d", i)
							assert.Equal(t, want.primaryKey, out.primaryKey, "key node %d", i)
							assert.True(t, bytes.Equal(expectedPrefix(want.value, prefixLen), out.value), "value node %d", i)
							assert.Equal(t, normalizeSecondaryKeys(want.secondaryKeys),
								normalizeSecondaryKeys(out.secondaryKeys[:secCount]), "secondary keys node %d", i)
							if prefixLen > 0 {
								assert.LessOrEqual(t, cap(out.value), prefixLen,
									"digest parser must not allocate beyond the prefix")
							}
						}
						_, err := r.Read(make([]byte, 1))
						assert.ErrorIs(t, err, io.EOF, "all bytes must be consumed")
					})
				}

				t.Run("mmap", func(t *testing.T) {
					var out segmentReplaceNode
					rw := byteops.NewReadWriter(nil)
					pos := 0
					for i, want := range nodes {
						rw.ResetBuffer(data[pos:])
						require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, secCount, prefixLen, &out), "node %d", i)

						var full segmentReplaceNode
						fullRW := byteops.NewReadWriter(data[pos:])
						require.NoError(t, ParseReplaceNodeIntoMMAP(&fullRW, secCount, &full))

						assert.Equal(t, full.offset, out.offset, "offset node %d", i)
						assert.Equal(t, fullOffsets[i], out.offset, "offset vs pread node %d", i)
						assert.Equal(t, want.tombstone, out.tombstone, "tombstone node %d", i)
						assert.Equal(t, want.primaryKey, out.primaryKey, "key node %d", i)
						assert.True(t, bytes.Equal(expectedPrefix(want.value, prefixLen), out.value), "value node %d", i)
						assert.Equal(t, normalizeSecondaryKeys(want.secondaryKeys),
							normalizeSecondaryKeys(out.secondaryKeys), "secondary keys node %d", i)
						if prefixLen > 0 {
							assert.LessOrEqual(t, cap(out.value), prefixLen,
								"digest parser must not allocate beyond the prefix")
						}
						pos += out.offset
					}
					assert.Equal(t, len(data), pos)
				})
			})
		}
	}
}

func TestParseReplaceNodeDigest_ValueIsCopy(t *testing.T) {
	node := digestTestNode(100, false, 0, 7)
	data := serializeDigestTestNodes(t, []segmentReplaceNode{node})

	var out segmentReplaceNode
	rw := byteops.NewReadWriter(data)
	require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, 0, 42, &out))
	want := copySliceDigest(out.value)

	// mutate the underlying buffer; the retained prefix must be unaffected,
	// matching the full MMAP parser which also copies the value.
	for i := range data {
		data[i] = 0xAB
	}
	assert.Equal(t, want, out.value)
}

func TestParseReplaceNodeDigest_TruncatedValue(t *testing.T) {
	node := digestTestNode(1000, false, 0, 1)
	data := serializeDigestTestNodes(t, []segmentReplaceNode{node})
	// cut in the middle of the discarded remainder of the value
	truncated := data[:9+500]

	for _, rf := range digestReaderFactories {
		t.Run("pread/"+rf.name, func(t *testing.T) {
			var full, digest segmentReplaceNode
			require.Error(t, ParseReplaceNodeIntoPread(rf.make(truncated), 0, &full))
			require.Error(t, ParseReplaceNodeDigestIntoPread(rf.make(truncated), 0, 42, &digest))
		})
	}

	t.Run("mmap", func(t *testing.T) {
		var out segmentReplaceNode
		rw := byteops.NewReadWriter(truncated)
		require.Error(t, ParseReplaceNodeDigestIntoMMAP(&rw, 0, 42, &out))
	})
}

func TestPreadSkipReader_Discard(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}

	tests := []struct {
		name    string
		bufSize int
		steps   []int // alternating: read n, discard n, read n, ...
	}{
		{name: "discard within buffer", bufSize: 64, steps: []int{4, 10, 4}},
		{name: "discard beyond buffer", bufSize: 16, steps: []int{4, 500, 8}},
		{name: "discard exactly buffered", bufSize: 16, steps: []int{4, 12, 8}},
		{name: "discard at start", bufSize: 16, steps: []int{0, 700, 16}},
		{name: "many small", bufSize: 16, steps: []int{1, 1, 1, 30, 2, 100, 3, 5, 7}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			or := &offsetReader{ra: bytes.NewReader(data)}
			r := &preadSkipReader{or: or, br: bufio.NewReaderSize(or, tt.bufSize)}
			pos := 0
			for i, n := range tt.steps {
				if i%2 == 0 {
					got := make([]byte, n)
					_, err := io.ReadFull(r, got)
					require.NoError(t, err)
					assert.Equal(t, data[pos:pos+n], got, "step %d", i)
				} else {
					discarded, err := r.Discard(n)
					require.NoError(t, err)
					assert.Equal(t, n, discarded)
				}
				pos += n
			}
		})
	}
}

func copySliceDigest(src []byte) []byte {
	dst := make([]byte, len(src))
	copy(dst, src)
	return dst
}
