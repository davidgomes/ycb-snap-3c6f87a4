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
	"encoding/binary"
	"fmt"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/entities/storobj"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func digestTestNodes(secondaryIndexCount uint16) []segmentReplaceNode {
	valueOfLen := func(n int, seed byte) []byte {
		v := make([]byte, n)
		for i := range v {
			v[i] = seed + byte(i)
		}
		return v
	}
	secKeys := func(i int) [][]byte {
		if secondaryIndexCount == 0 {
			return nil
		}
		keys := make([][]byte, secondaryIndexCount)
		for j := range keys {
			if (i+j)%2 == 0 {
				keys[j] = []byte(fmt.Sprintf("sec-%d-%d", i, j))
			}
		}
		return keys
	}

	// Sizes deliberately alternate large/small so buffer reuse across
	// iterations is exercised in both directions.
	sizes := []int{10_000, 0, 5, storobj.MarshallerV1HeaderLen - 1, 20_000,
		storobj.MarshallerV1HeaderLen, storobj.MarshallerV1HeaderLen + 1, 1, 6_000}
	nodes := make([]segmentReplaceNode, 0, len(sizes)+1)
	for i, size := range sizes {
		nodes = append(nodes, segmentReplaceNode{
			value:               valueOfLen(size, byte(i)),
			primaryKey:          []byte(fmt.Sprintf("key-%03d", i)),
			secondaryIndexCount: secondaryIndexCount,
			secondaryKeys:       secKeys(i),
		})
	}
	nodes = append(nodes, segmentReplaceNode{
		tombstone:           true,
		value:               valueOfLen(300, 7),
		primaryKey:          []byte("key-tomb"),
		secondaryIndexCount: secondaryIndexCount,
		secondaryKeys:       secKeys(len(sizes)),
	})
	return nodes
}

func serializeDigestTestNodes(t *testing.T, nodes []segmentReplaceNode) ([]byte, []int) {
	t.Helper()
	var buf bytes.Buffer
	starts := make([]int, len(nodes))
	for i := range nodes {
		starts[i] = buf.Len()
		_, err := nodes[i].KeyIndexAndWriteTo(&buf)
		require.NoError(t, err)
	}
	return buf.Bytes(), starts
}

type digestParseFn func(data []byte, start int, secondaryIndexCount uint16, out *segmentReplaceNode) error

func digestParsers(valuePrefixLen int) map[string]struct{ full, digest digestParseFn } {
	mmapFull := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		rw := byteops.NewReadWriter(data[start:])
		return ParseReplaceNodeIntoMMAP(&rw, sic, out)
	}
	mmapDigest := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		rw := byteops.NewReadWriter(data[start:])
		return ParseReplaceNodeDigestIntoMMAP(&rw, sic, out, valuePrefixLen)
	}
	preadFull := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		return ParseReplaceNodeIntoPread(bufio.NewReader(bytes.NewReader(data[start:])), sic, out)
	}
	preadDigest := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		return ParseReplaceNodeDigestIntoPread(bufio.NewReader(bytes.NewReader(data[start:])), sic, out, valuePrefixLen)
	}
	// plain reader without Discard exercises the non-bufio skip fallback
	preadPlainFull := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		return ParseReplaceNodeIntoPread(bytes.NewReader(data[start:]), sic, out)
	}
	preadPlainDigest := func(data []byte, start int, sic uint16, out *segmentReplaceNode) error {
		return ParseReplaceNodeDigestIntoPread(bytes.NewReader(data[start:]), sic, out, valuePrefixLen)
	}

	return map[string]struct{ full, digest digestParseFn }{
		"mmap":        {mmapFull, mmapDigest},
		"pread_bufio": {preadFull, preadDigest},
		"pread_plain": {preadPlainFull, preadPlainDigest},
	}
}

func TestParseReplaceNodeDigestInto_ParityWithFullParsers(t *testing.T) {
	prefixLens := []int{0, -1, 1, storobj.MarshallerV1HeaderLen, 1 << 20}

	for _, secondaryIndexCount := range []uint16{0, 2} {
		nodes := digestTestNodes(secondaryIndexCount)
		data, starts := serializeDigestTestNodes(t, nodes)

		for _, prefixLen := range prefixLens {
			for name, p := range digestParsers(prefixLen) {
				t.Run(fmt.Sprintf("sec=%d/prefix=%d/%s", secondaryIndexCount, prefixLen, name), func(t *testing.T) {
					// Both nodes are reused across all iterations, as the
					// reusable cursor does.
					var full, digest segmentReplaceNode

					// Walk the nodes the way a cursor does: next start is
					// current start + out.offset.
					fullPos, digestPos := 0, 0
					for i, want := range nodes {
						require.NoError(t, p.full(data, fullPos, secondaryIndexCount, &full))
						require.NoError(t, p.digest(data, digestPos, secondaryIndexCount, &digest))

						assert.Equal(t, full.offset, digest.offset, "node %d: offset", i)
						assert.Equal(t, full.tombstone, digest.tombstone, "node %d: tombstone", i)
						assert.Equal(t, want.tombstone, digest.tombstone, "node %d: tombstone", i)
						assert.Equal(t, full.primaryKey, digest.primaryKey, "node %d: key", i)
						assert.Equal(t, want.primaryKey, digest.primaryKey, "node %d: key", i)
						for j := 0; j < int(secondaryIndexCount); j++ {
							assert.Equal(t, len(want.secondaryKeys[j]), len(digest.secondaryKeys[j]), "node %d: sec key %d", i, j)
							assert.Equal(t, string(full.secondaryKeys[j]), string(digest.secondaryKeys[j]), "node %d: sec key %d", i, j)
						}

						assert.Equal(t, want.value, full.value, "node %d: full value", i)
						wantValue := want.value
						if prefixLen > 0 && len(wantValue) > prefixLen {
							wantValue = wantValue[:prefixLen]
						}
						assert.Equal(t, wantValue, digest.value, "node %d: digest value", i)

						fullPos += full.offset
						digestPos += digest.offset
						if i+1 < len(nodes) {
							require.Equal(t, starts[i+1], digestPos, "node %d: next node start", i)
						}
					}
					assert.Equal(t, len(data), fullPos)
					assert.Equal(t, len(data), digestPos)
				})
			}
		}
	}
}

func TestParseReplaceNodeDigestInto_TruncatedValue(t *testing.T) {
	node := segmentReplaceNode{value: make([]byte, 1000), primaryKey: []byte("k")}
	var buf bytes.Buffer
	_, err := node.KeyIndexAndWriteTo(&buf)
	require.NoError(t, err)
	// cut inside the value, past the retained prefix
	truncated := buf.Bytes()[:9+500]

	for name, p := range digestParsers(storobj.MarshallerV1HeaderLen) {
		t.Run(name, func(t *testing.T) {
			var out segmentReplaceNode
			err := p.digest(truncated, 0, 0, &out)
			require.Error(t, err)
		})
	}
}

func TestParseReplaceNodeDigestInto_ValueLengthOverflow(t *testing.T) {
	data := make([]byte, 9+4+1)
	binary.LittleEndian.PutUint64(data[1:9], ^uint64(0))

	var out segmentReplaceNode
	rw := byteops.NewReadWriter(data)
	require.Error(t, ParseReplaceNodeDigestIntoMMAP(&rw, 0, &out, storobj.MarshallerV1HeaderLen))

	require.Error(t, ParseReplaceNodeDigestIntoPread(bufio.NewReader(bytes.NewReader(data)), 0, &out,
		storobj.MarshallerV1HeaderLen))
}

// TestParseReplaceNodeDigestInto_NoValueAllocation pins that the value tail is
// skipped without allocating it once the reusable node's buffers are warm.
func TestParseReplaceNodeDigestInto_NoValueAllocation(t *testing.T) {
	node := segmentReplaceNode{value: make([]byte, 64_000), primaryKey: []byte("some-key")}
	var buf bytes.Buffer
	_, err := node.KeyIndexAndWriteTo(&buf)
	require.NoError(t, err)
	data := buf.Bytes()

	const runs = 100
	bytesPerRun := func(f func()) uint64 {
		f() // warm up reusable buffers
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		for i := 0; i < runs; i++ {
			f()
		}
		runtime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / runs
	}

	t.Run("mmap", func(t *testing.T) {
		var out segmentReplaceNode
		rw := byteops.NewReadWriter(nil)
		allocated := bytesPerRun(func() {
			rw.ResetBuffer(data)
			if err := ParseReplaceNodeDigestIntoMMAP(&rw, 0, &out, storobj.MarshallerV1HeaderLen); err != nil {
				t.Fatal(err)
			}
		})
		assert.Less(t, allocated, uint64(1024))
		assert.Equal(t, storobj.MarshallerV1HeaderLen, cap(out.value))
		assert.Equal(t, len(data), out.offset)
	})

	t.Run("pread", func(t *testing.T) {
		var out segmentReplaceNode
		or := &offsetReader{ra: bytes.NewReader(data)}
		br := bufio.NewReader(or)
		allocated := bytesPerRun(func() {
			or.off = 0
			br.Reset(or)
			if err := ParseReplaceNodeDigestIntoPread(br, 0, &out, storobj.MarshallerV1HeaderLen); err != nil {
				t.Fatal(err)
			}
		})
		assert.Less(t, allocated, uint64(1024))
		assert.Equal(t, storobj.MarshallerV1HeaderLen, cap(out.value))
		assert.Equal(t, len(data), out.offset)
	})
}
