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
	"github.com/weaviate/weaviate/entities/storobj"
	"github.com/weaviate/weaviate/usecases/byteops"
)

// digestPreadReaders are the reader shapes ParseReplaceNodeDigestIntoPread must
// handle: a *bufio.Reader (Discard), a reader without Discard (copy fallback),
// and the reusable cursor's preadSkipReader (offset skip). Small buffers force
// skips to cross buffer boundaries.
var digestPreadReaders = []struct {
	name string
	new  func(data []byte) io.Reader
}{
	{"bufio", func(data []byte) io.Reader {
		return bufio.NewReaderSize(bytes.NewReader(data), 16)
	}},
	{"plain", func(data []byte) io.Reader {
		return struct{ io.Reader }{bytes.NewReader(data)}
	}},
	{"skip", func(data []byte) io.Reader {
		src := &offsetReader{ra: bytes.NewReader(data)}
		return &preadSkipReader{buf: bufio.NewReaderSize(src, 16), src: src}
	}},
}

func digestTestValue(n int, seed byte) []byte {
	v := make([]byte, n)
	for i := range v {
		v[i] = seed + byte(i*7)
	}
	return v
}

func encodeReplaceNode(t *testing.T, n segmentReplaceNode) []byte {
	t.Helper()
	var buf bytes.Buffer
	_, err := n.KeyIndexAndWriteTo(&buf)
	require.NoError(t, err)
	return buf.Bytes()
}

func expectedDigestValue(full []byte, prefix int) []byte {
	if prefix <= 0 || prefix >= len(full) {
		return full
	}
	return full[:prefix]
}

// assertDigestNodeMatches compares byte fields by content: the pread and mmap
// full parsers already disagree on nil vs empty for empty keys.
func assertDigestNodeMatches(t *testing.T, want, got *segmentReplaceNode, secondaryIndexCount uint16, prefix int) {
	t.Helper()
	assert.Equal(t, want.offset, got.offset, "offset must span the whole node")
	assert.Equal(t, want.tombstone, got.tombstone)
	assert.Equal(t, string(want.primaryKey), string(got.primaryKey))
	assert.Equal(t, string(expectedDigestValue(want.value, prefix)), string(got.value))
	require.GreaterOrEqual(t, len(got.secondaryKeys), int(secondaryIndexCount))
	for j := 0; j < int(secondaryIndexCount); j++ {
		assert.Equal(t, string(want.secondaryKeys[j]), string(got.secondaryKeys[j]), "secondary key %d", j)
	}
}

func TestParseReplaceNodeDigest_MatchesFullParsers(t *testing.T) {
	headerLen := storobj.MarshallerV1HeaderLen

	nodes := []struct {
		name string
		node segmentReplaceNode
	}{
		{"empty value", segmentReplaceNode{primaryKey: []byte("k")}},
		{"one byte value", segmentReplaceNode{primaryKey: []byte("k"), value: digestTestValue(1, 1)}},
		{"five byte value", segmentReplaceNode{primaryKey: []byte("k5"), value: digestTestValue(5, 2)}},
		{"header minus one", segmentReplaceNode{primaryKey: []byte("key"), value: digestTestValue(headerLen-1, 3)}},
		{"exactly header", segmentReplaceNode{primaryKey: []byte("key"), value: digestTestValue(headerLen, 4)}},
		{"header plus one", segmentReplaceNode{primaryKey: []byte("key"), value: digestTestValue(headerLen+1, 5)}},
		{"empty primary key", segmentReplaceNode{value: digestTestValue(300, 6)}},
		{
			"large value with secondary key",
			segmentReplaceNode{
				primaryKey:          []byte("uuid-0000000001"),
				value:               digestTestValue(10_000, 7),
				secondaryIndexCount: 1,
				secondaryKeys:       [][]byte{[]byte("docid-01")},
			},
		},
		{
			"tombstone with value and empty secondary key",
			segmentReplaceNode{
				tombstone:           true,
				primaryKey:          []byte("deleted-key"),
				value:               digestTestValue(100_000, 8),
				secondaryIndexCount: 2,
				secondaryKeys:       [][]byte{[]byte("sec-0"), nil},
			},
		},
		{
			"tombstone without value",
			segmentReplaceNode{
				tombstone:           true,
				primaryKey:          []byte("gone"),
				secondaryIndexCount: 1,
				secondaryKeys:       [][]byte{[]byte("docid-02")},
			},
		},
	}

	prefixes := []int{-1, 0, 1, headerLen - 1, headerLen, headerLen + 1, 4096, 1 << 30}

	for _, nc := range nodes {
		encoded := encodeReplaceNode(t, nc.node)
		count := nc.node.secondaryIndexCount

		var want segmentReplaceNode
		require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(encoded), count, &want))
		require.Equal(t, len(encoded), want.offset)

		var wantMMAP segmentReplaceNode
		rw := byteops.NewReadWriter(encoded)
		require.NoError(t, ParseReplaceNodeIntoMMAP(&rw, count, &wantMMAP))
		require.Equal(t, want.offset, wantMMAP.offset, "full parsers must agree")

		for _, prefix := range prefixes {
			t.Run(fmt.Sprintf("%s/prefix=%d", nc.name, prefix), func(t *testing.T) {
				for _, rc := range digestPreadReaders {
					t.Run("pread/"+rc.name, func(t *testing.T) {
						var got segmentReplaceNode
						require.NoError(t, ParseReplaceNodeDigestIntoPread(rc.new(encoded), count, prefix, &got))
						assertDigestNodeMatches(t, &want, &got, count, prefix)
						if prefix > 0 {
							assert.LessOrEqual(t, cap(got.value), prefix,
								"the value remainder must never be allocated")
						}
					})
				}

				t.Run("mmap", func(t *testing.T) {
					var got segmentReplaceNode
					rw := byteops.NewReadWriter(encoded)
					require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, count, prefix, &got))
					assertDigestNodeMatches(t, &want, &got, count, prefix)
					assert.Equal(t, uint64(len(encoded)), rw.Position)
					if prefix > 0 {
						assert.LessOrEqual(t, cap(got.value), prefix,
							"the value remainder must never be allocated")
					}
				})
			})
		}
	}
}

// TestParseReplaceNodeDigest_SequentialReuse parses a run of back-to-back nodes
// of wildly varying value sizes with a single reused out node, the way the
// reusable cursor does. Every parse must leave the stream exactly at the next
// node and must not leak bytes of a previous (longer) value.
func TestParseReplaceNodeDigest_SequentialReuse(t *testing.T) {
	headerLen := storobj.MarshallerV1HeaderLen
	valueLens := []int{100_000, 3, 0, headerLen, 50_000, headerLen + 1, 10, 4096, 1}

	var all []byte
	nodes := make([]segmentReplaceNode, len(valueLens))
	for i, l := range valueLens {
		nodes[i] = segmentReplaceNode{
			tombstone:           i%4 == 3,
			primaryKey:          []byte(fmt.Sprintf("key-%03d", i)),
			value:               digestTestValue(l, byte(i)),
			secondaryIndexCount: 1,
			secondaryKeys:       [][]byte{[]byte(fmt.Sprintf("sec-%d", i))},
		}
		all = append(all, encodeReplaceNode(t, nodes[i])...)
	}

	for _, prefix := range []int{0, 1, headerLen, 1 << 20} {
		t.Run(fmt.Sprintf("prefix=%d", prefix), func(t *testing.T) {
			for _, rc := range digestPreadReaders {
				t.Run("pread/"+rc.name, func(t *testing.T) {
					r := rc.new(all)
					var out segmentReplaceNode
					total := 0
					for i, n := range nodes {
						require.NoError(t, ParseReplaceNodeDigestIntoPread(r, 1, prefix, &out), "node %d", i)
						assert.Equal(t, n.tombstone, out.tombstone, "node %d", i)
						assert.Equal(t, n.primaryKey, out.primaryKey, "node %d", i)
						assert.Equal(t, expectedDigestValue(n.value, prefix), out.value, "node %d", i)
						assert.Equal(t, n.secondaryKeys[0], out.secondaryKeys[0], "node %d", i)
						total += out.offset
					}
					assert.Equal(t, len(all), total)
					if prefix > 0 {
						assert.LessOrEqual(t, cap(out.value), prefix)
					}
					require.ErrorIs(t, ParseReplaceNodeDigestIntoPread(r, 1, prefix, &out), io.EOF,
						"the last node must end exactly at the end of the stream")
				})
			}

			t.Run("mmap", func(t *testing.T) {
				rw := byteops.NewReadWriter(nil)
				var out segmentReplaceNode
				offset := 0
				for i, n := range nodes {
					rw.ResetBuffer(all[offset:])
					require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, 1, prefix, &out), "node %d", i)
					assert.Equal(t, n.tombstone, out.tombstone, "node %d", i)
					assert.Equal(t, n.primaryKey, out.primaryKey, "node %d", i)
					assert.Equal(t, expectedDigestValue(n.value, prefix), out.value, "node %d", i)
					assert.Equal(t, n.secondaryKeys[0], out.secondaryKeys[0], "node %d", i)
					offset += out.offset
				}
				assert.Equal(t, len(all), offset)
				if prefix > 0 {
					assert.LessOrEqual(t, cap(out.value), prefix)
				}
			})
		})
	}
}

// TestParseReplaceNodeDigestIntoPread_Truncated verifies a node cut off inside
// the skipped value remainder or right after it fails like the full parser
// does, instead of silently yielding a node.
func TestParseReplaceNodeDigestIntoPread_Truncated(t *testing.T) {
	headerLen := storobj.MarshallerV1HeaderLen
	node := segmentReplaceNode{
		primaryKey: []byte("truncated-key"),
		value:      digestTestValue(1_000, 9),
	}
	encoded := encodeReplaceNode(t, node)

	cuts := []struct {
		name string
		at   int
	}{
		{"inside value prefix", 9 + headerLen/2},
		{"inside value remainder", 9 + 500},
		{"right after value", 9 + 1_000},
		{"inside key length", 9 + 1_000 + 2},
		{"inside key", len(encoded) - 1},
	}

	for _, cut := range cuts {
		t.Run(cut.name, func(t *testing.T) {
			truncated := encoded[:cut.at]

			var full segmentReplaceNode
			require.Error(t, ParseReplaceNodeIntoPread(bytes.NewReader(truncated), 0, &full))

			for _, rc := range digestPreadReaders {
				t.Run(rc.name, func(t *testing.T) {
					var got segmentReplaceNode
					require.Error(t, ParseReplaceNodeDigestIntoPread(rc.new(truncated), 0, headerLen, &got))
				})
			}
		})
	}
}

// TestParseReplaceNodeDigest_AllocationsIndependentOfValueSize guards the
// point of the digest parsers: with a warm out node, parsing a node with a huge
// value costs no more allocations than parsing one with a tiny value.
func TestParseReplaceNodeDigest_AllocationsIndependentOfValueSize(t *testing.T) {
	headerLen := storobj.MarshallerV1HeaderLen
	small := encodeReplaceNode(t, segmentReplaceNode{
		primaryKey: []byte("small"), value: digestTestValue(headerLen+2, 1),
		secondaryIndexCount: 1, secondaryKeys: [][]byte{[]byte("s")},
	})
	large := encodeReplaceNode(t, segmentReplaceNode{
		primaryKey: []byte("large"), value: digestTestValue(1<<20, 2),
		secondaryIndexCount: 1, secondaryKeys: [][]byte{[]byte("l")},
	})

	t.Run("mmap", func(t *testing.T) {
		allocs := func(data []byte) float64 {
			rw := byteops.NewReadWriter(nil)
			out := segmentReplaceNode{secondaryKeys: make([][]byte, 1)}
			return testing.AllocsPerRun(20, func() {
				rw.ResetBuffer(data)
				if err := ParseReplaceNodeDigestIntoMMAP(&rw, 1, headerLen, &out); err != nil {
					t.Fatal(err)
				}
			})
		}
		assert.Zero(t, allocs(small))
		assert.Zero(t, allocs(large))
	})

	t.Run("pread", func(t *testing.T) {
		allocs := func(data []byte) float64 {
			ra := bytes.NewReader(data)
			src := &offsetReader{ra: ra}
			r := &preadSkipReader{buf: bufio.NewReader(src), src: src}
			out := segmentReplaceNode{secondaryKeys: make([][]byte, 1)}
			return testing.AllocsPerRun(20, func() {
				src.off = 0
				r.buf.Reset(src)
				if err := ParseReplaceNodeDigestIntoPread(r, 1, headerLen, &out); err != nil {
					t.Fatal(err)
				}
			})
		}
		assert.Equal(t, allocs(small), allocs(large))
	})
}
