// Copyright 2014-2026 Ulrich Kunitz. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

/*
Package redundancy provides cheap, single-pass heuristics for estimating
how redundant -- and therefore how likely to compress well with an
LZ-style algorithm such as LZMA -- a block of data is.

The heuristics in this package do not perform any compression
themselves. They exist so that a caller (for example a future adaptive
LZMA writer, see https://github.com/unxed/zipper/issues/20) can decide
inexpensively whether it is worth spending more CPU time -- a bigger
dictionary, a slower but stronger match algorithm -- on a given block,
or whether it should do the opposite and drop to a cheaper
configuration because the block looks close to incompressible (e.g.
already-compressed or encrypted data).

Two independent signals are provided, because they catch different
failure modes:

  - Entropy is the order-0 Shannon entropy of the byte distribution. It
    is a good, cheap proxy for "this data is already dense/high-entropy
    and unlikely to compress further" (already-compressed data,
    encrypted data, most media formats). It is a poor proxy for
    LZ-style redundancy: a block built by repeating the same 100
    pseudo-random bytes many times has byte-frequency entropy close to
    that of random data, even though a match finder would collapse it
    almost to nothing.

  - DuplicateRatio approximates the fraction of fixed-length windows in
    the block that repeat a window seen earlier in the same block,
    using the same kind of rolling hash (see internal/hash) the LZMA
    match finder itself uses to index the dictionary. It responds
    directly to repeated substrings, which is what an LZ77-style
    compressor actually exploits, and so complements Entropy.

EstimateRedundancy combines both signals into a single score in [0, 1]
for convenience; callers that want to weight the signals differently, or
tune them against real data, should call Analyze and look at the
individual fields instead.

This package intentionally does not decide anything about compression
parameters (dictionary size, match algorithm, LZMA properties): wiring
its output into an actual parameter choice, and validating the result
against real data, is left to follow-up work (see the issue above).
*/
package redundancy
