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
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/weaviate/weaviate/usecases/byteops"
)

func TestParseReplaceNodeDigest_MatchesFullParser(t *testing.T) {
	tests := []struct {
		name      string
		tombstone bool
		value     []byte
		key       []byte
		secondary [][]byte
		prefix    int
	}{
		{
			name:      "prefix shorter than value",
			value:     bytes.Repeat([]byte{'V'}, 1000),
			key:       []byte("primary-key"),
			secondary: [][]byte{[]byte("sec-a"), {}, []byte("sec-ccc")},
			prefix:    42,
		},
		{
			name:      "prefix longer than value",
			value:     []byte("short"),
			key:       []byte("k"),
			secondary: [][]byte{[]byte("s")},
			prefix:    100,
		},
		{
			name:   "prefix equals value",
			value:  bytes.Repeat([]byte{'E'}, 16),
			key:    []byte("eq"),
			prefix: 16,
		},
		{
			name:   "prefix zero reads full value",
			value:  bytes.Repeat([]byte{'Z'}, 200),
			key:    []byte("zero"),
			prefix: 0,
		},
		{
			name:   "negative prefix reads full value",
			value:  bytes.Repeat([]byte{'N'}, 80),
			key:    []byte("neg"),
			prefix: -1,
		},
		{
			name:      "empty tombstone value",
			tombstone: true,
			key:       []byte("empty"),
			secondary: [][]byte{{}},
			prefix:    42,
		},
		{
			name:   "empty key",
			value:  bytes.Repeat([]byte{'K'}, 50),
			key:    []byte{},
			prefix: 8,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			enc := encodeReplaceNode(t, tc.tombstone, tc.value, tc.key, tc.secondary)
			secCount := uint16(len(tc.secondary))

			full, err := ParseReplaceNode(bytes.NewReader(enc), secCount)
			require.NoError(t, err)
			require.Equal(t, len(enc), full.offset)

			var pread segmentReplaceNode
			require.NoError(t, ParseReplaceNodeIntoPread(bytes.NewReader(enc), secCount, &pread))

			var digest segmentReplaceNode
			require.NoError(t, ParseReplaceNodeDigestIntoPread(bytes.NewReader(enc), secCount, tc.prefix, &digest))

			assertReplaceNodeParity(t, full, pread, digest, tc.prefix)

			rw := byteops.NewReadWriter(enc)
			var mmapFull segmentReplaceNode
			require.NoError(t, ParseReplaceNodeIntoMMAP(&rw, secCount, &mmapFull))
			require.Equal(t, len(enc), mmapFull.offset)
			require.Equal(t, uint64(len(enc)), rw.Position)

			rw.ResetBuffer(enc)
			var mmapDigest segmentReplaceNode
			require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, secCount, tc.prefix, &mmapDigest))
			require.Equal(t, mmapFull.offset, mmapDigest.offset)
			require.Equal(t, uint64(len(enc)), rw.Position)
			require.Equal(t, mmapFull.tombstone, mmapDigest.tombstone)
			requireSameBytes(t, mmapFull.primaryKey, mmapDigest.primaryKey)
			requireSameBytes(t, expectedDigestValue(full.value, tc.prefix), mmapDigest.value)
			requireSecondaryKeysEqual(t, mmapFull.secondaryKeys, mmapDigest.secondaryKeys)

			if tc.prefix > 0 && len(tc.value) > tc.prefix {
				require.LessOrEqual(t, cap(digest.value), tc.prefix)
				require.LessOrEqual(t, cap(mmapDigest.value), tc.prefix)
			}
		})
	}
}

func TestParseReplaceNodeDigest_SequentialNodesAndBufferReuse(t *testing.T) {
	const prefix = 16
	first := encodeReplaceNode(t, false, bytes.Repeat([]byte{'A'}, 5_000), []byte("key-a"), [][]byte{bytes.Repeat([]byte{'s'}, 20), {}})
	second := encodeReplaceNode(t, true, []byte("short-b"), []byte("key-b-longer"), [][]byte{[]byte("s"), bytes.Repeat([]byte{'t'}, 50)})
	third := encodeReplaceNode(t, false, bytes.Repeat([]byte{'C'}, prefix), []byte("key-c"), [][]byte{bytes.Repeat([]byte{'u'}, 20), []byte("mid")})
	enc := append(append(append([]byte{}, first...), second...), third...)

	// bytes.Reader has no Skip: the tail is read and dropped, and the reader
	// still lands on the next node.
	r := bytes.NewReader(enc)
	var node segmentReplaceNode
	require.NoError(t, ParseReplaceNodeDigestIntoPread(r, 2, prefix, &node))
	require.Equal(t, len(first), node.offset)
	require.Equal(t, bytes.Repeat([]byte{'A'}, prefix), append([]byte(nil), node.value...))
	require.Equal(t, prefix, cap(node.value))
	valuePtr := &node.value[0]
	secPtr := &node.secondaryKeys[0][0]

	require.NoError(t, ParseReplaceNodeDigestIntoPread(r, 2, prefix, &node))
	require.Equal(t, len(second), node.offset)
	require.Equal(t, []byte("short-b"), append([]byte(nil), node.value...))
	require.Equal(t, []byte("key-b-longer"), append([]byte(nil), node.primaryKey...))
	require.True(t, node.tombstone)
	require.Equal(t, []byte("s"), append([]byte(nil), node.secondaryKeys[0]...))
	require.Equal(t, bytes.Repeat([]byte{'t'}, 50), append([]byte(nil), node.secondaryKeys[1]...))
	require.Equal(t, prefix, cap(node.value))

	require.NoError(t, ParseReplaceNodeDigestIntoPread(r, 2, prefix, &node))
	require.Equal(t, len(third), node.offset)
	require.Equal(t, bytes.Repeat([]byte{'C'}, prefix), node.value)
	require.Equal(t, []byte("key-c"), append([]byte(nil), node.primaryKey...))
	require.False(t, node.tombstone)
	require.Equal(t, bytes.Repeat([]byte{'u'}, 20), node.secondaryKeys[0])
	require.Equal(t, []byte("mid"), append([]byte(nil), node.secondaryKeys[1]...))
	require.Equal(t, prefix, cap(node.value))
	require.Equal(t, valuePtr, &node.value[0])
	require.Equal(t, secPtr, &node.secondaryKeys[0][0])
	require.Zero(t, r.Len())

	// Same bytes through the mmap parser, resetting like the cursor does.
	rw := byteops.NewReadWriter(nil)
	var mmapNode segmentReplaceNode
	off := 0
	for _, part := range [][]byte{first, second, third} {
		rw.ResetBuffer(enc[off:])
		require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, 2, prefix, &mmapNode))
		require.Equal(t, len(part), mmapNode.offset)
		require.Equal(t, uint64(len(part)), rw.Position)
		off += len(part)
	}
	require.Equal(t, len(enc), off)
	require.Equal(t, bytes.Repeat([]byte{'C'}, prefix), mmapNode.value)
	require.Equal(t, []byte("key-c"), mmapNode.primaryKey)
	require.LessOrEqual(t, cap(mmapNode.value), prefix)
}

func TestParseReplaceNodeDigestIntoPread_SkipsValueTail(t *testing.T) {
	const prefix = 42
	value := bytes.Repeat([]byte{'V'}, 100_000)
	key := []byte("object-key")
	secondary := [][]byte{[]byte("secondary-key")}
	enc := encodeReplaceNode(t, false, value, key, secondary)

	ra := &recordingReaderAt{buf: enc}
	or := &offsetReader{ra: ra}
	var node segmentReplaceNode
	require.NoError(t, ParseReplaceNodeDigestIntoPread(or, 1, prefix, &node))

	require.Equal(t, len(enc), node.offset)
	require.Equal(t, value[:prefix], node.value)
	require.Equal(t, key, node.primaryKey)
	require.Equal(t, secondary[0], node.secondaryKeys[0])
	require.Equal(t, int64(len(enc)), or.off)

	wantRead := 9 + prefix + 4 + len(key) + 4 + len(secondary[0])
	require.Equal(t, wantRead, ra.readN)
	require.Less(t, ra.readN, len(value))

	// A second node appended after the first is parsed from the seeked offset.
	second := encodeReplaceNode(t, false, []byte("tiny"), []byte("next"), nil)
	ra.buf = append(enc, second...)
	require.NoError(t, ParseReplaceNodeDigestIntoPread(or, 0, prefix, &node))
	require.Equal(t, []byte("next"), append([]byte(nil), node.primaryKey...))
	require.Equal(t, []byte("tiny"), append([]byte(nil), node.value...))
	require.Equal(t, len(second), node.offset)
	require.Equal(t, wantRead+len(second), ra.readN)
}

func TestParseReplaceNodeDigestIntoMMAP_TruncatedValueTail(t *testing.T) {
	const prefix = 42
	value := bytes.Repeat([]byte{'V'}, 1000)
	enc := encodeReplaceNode(t, false, value, []byte("key"), nil)
	// Header and the retained prefix are present; the value tail is not.
	truncated := enc[:9+prefix]

	rw := byteops.NewReadWriter(truncated)
	var node segmentReplaceNode
	err := ParseReplaceNodeDigestIntoMMAP(&rw, 0, prefix, &node)
	require.Error(t, err)

	// The pread path reports the same short read instead of inventing a key.
	err = ParseReplaceNodeDigestIntoPread(bytes.NewReader(truncated), 0, prefix, &node)
	require.Error(t, err)
}

func TestParseReplaceNodeDigest_ReusesValueBufferAcrossSizes(t *testing.T) {
	const prefix = 16
	var pread segmentReplaceNode
	var mmap segmentReplaceNode
	var valuePtr *byte

	sizes := []int{4, 1_000, 8, prefix, 3, 100, 1, prefix}
	for _, sz := range sizes {
		value := bytes.Repeat([]byte{byte(sz)}, sz)
		enc := encodeReplaceNode(t, false, value, []byte("fixed-key"), nil)

		require.NoError(t, ParseReplaceNodeDigestIntoPread(bytes.NewReader(enc), 0, prefix, &pread))
		keep := sz
		if keep > prefix {
			keep = prefix
		}
		require.Equal(t, value[:keep], pread.value)
		require.LessOrEqual(t, cap(pread.value), prefix)
		if keep > 0 && valuePtr != nil {
			require.Equal(t, valuePtr, &pread.value[0])
		}
		if sz >= prefix {
			valuePtr = &pread.value[0]
			require.Equal(t, prefix, cap(pread.value))
		}

		rw := byteops.NewReadWriter(enc)
		require.NoError(t, ParseReplaceNodeDigestIntoMMAP(&rw, 0, prefix, &mmap))
		require.Equal(t, value[:keep], mmap.value)
		require.Equal(t, len(enc), mmap.offset)
		require.LessOrEqual(t, cap(mmap.value), prefix)
	}
}

func encodeReplaceNode(t *testing.T, tombstone bool, value, key []byte, secondary [][]byte) []byte {
	t.Helper()
	node := &segmentReplaceNode{
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

func expectedDigestValue(full []byte, prefix int) []byte {
	if prefix > 0 && len(full) > prefix {
		return full[:prefix]
	}
	return full
}

func assertReplaceNodeParity(t *testing.T, full, pread, digest segmentReplaceNode, prefix int) {
	t.Helper()
	require.Equal(t, full.offset, pread.offset)
	require.Equal(t, full.offset, digest.offset)
	require.Equal(t, full.tombstone, digest.tombstone)
	require.Equal(t, pread.tombstone, digest.tombstone)
	requireSameBytes(t, full.primaryKey, digest.primaryKey)
	requireSameBytes(t, pread.primaryKey, digest.primaryKey)
	requireSameBytes(t, expectedDigestValue(full.value, prefix), digest.value)
	requireSecondaryKeysEqual(t, full.secondaryKeys, digest.secondaryKeys)
	requireSecondaryKeysEqual(t, pread.secondaryKeys, digest.secondaryKeys)
}

func requireSameBytes(t *testing.T, want, got []byte) {
	t.Helper()
	require.Truef(t, bytes.Equal(want, got), "bytes mismatch\nwant %q\ngot  %q", want, got)
}

func requireSecondaryKeysEqual(t *testing.T, want, got [][]byte) {
	t.Helper()
	require.Equal(t, len(want), len(got))
	for i := range want {
		requireSameBytes(t, want[i], got[i])
	}
}

type recordingReaderAt struct {
	buf   []byte
	readN int
}

func (r *recordingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 || off >= int64(len(r.buf)) {
		return 0, io.EOF
	}
	n := copy(p, r.buf[off:])
	r.readN += n
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
