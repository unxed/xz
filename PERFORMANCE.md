# LZMA Decompression Optimization Log

This document tracks the attempts to optimize the LZMA decompression speed on Go.  
**Hardware for benchmarks:** Intel(R) Core(TM) i5-6300U CPU @ 2.40GHz (Linux/amd64).

---

## Baseline
*   **Performance:** ~14 MB/s
*   **State:** Idiomatic Go code, using interfaces (`io.ByteReader`), object allocations per operation (`readOp`), and standard circular buffer management.

---

## Attempt 1: Removing Boxing and Virtual Dispatch
*   **Changes:** 
    *   Eliminated interface boxing by replacing `readOp` (returning `operation` interface) with an allocation-free `processNextOp` method.
    *   Replaced the `io.ByteReader` interface inside the range decoder with a concrete, internally buffered `io.Reader`.
    *   Refactored `decoderDict.writeMatch` to use a flat, byte-by-byte circular copy loop.
    *   Added Bounds Check Elimination (BCE) hints.
*   **Result:** **20.24 MB/s**
*   **Commit:** `8b674c3a458e619b74a30a4869ab66b924f084ba`
*   **Conclusion:** Significant gain by reducing GC pressure and virtual method calls.

---

## Attempt 2: Register Caching and Manual Inlining
*   **Changes:**
    *   **State Caching:** The most critical Range Decoder variables (`nrange`, `code`, `pos`, `limit`) were lifted into local function scope within `processNextOp`.
    *   **Manual Inlining:** The bit-level logic (`DecodeBit`) was directly embedded into tree and literal decoding cycles.
    *   This forced the Go compiler to keep "hot" variables in CPU registers (`R8`-`R15`) instead of repeatedly reading from memory pointers.
*   **Result:** **27.25 MB/s**
*   **Commit:** `08f45adf27e7fb9737e9ac5f98569a322c571381`
*   **Conclusion:** **SUCCESS.** This provided the largest leap. The closer the code resembles a flat C-style loop, the better the Go compiler optimizes it.

---

## Attempt 3: "Supersonic" Flattening and Unsafe Pointers
*   **Changes:**
    *   **Flattening:** Merged all probability tables (isMatch, isRep, etc.) into one single `[]prob` array to maximize L1 cache hits.
    *   **Extensive Unsafe:** Replaced almost all array/slice indexing with `unsafe.Pointer` and `unsafe.Add`.
    *   **State Passing:** Introduced a `rangeState` struct to pass the register-cached state between helper methods.
*   **Result:** **23.50 MB/s (REGRESSION)**
*   **Status:** **FAILED / ROLLED BACK.**
*   **Post-Mortem:** 
    *   Even though "unsafe" sounds faster, it often breaks the Go compiler's ability to perform SSA (Static Single Assignment) optimizations and instruction reordering. 
    *   Passing a pointer to a struct (`rangeState`) into helper functions might have introduced enough overhead (or prevented enough inlining) to outweigh the benefits of a flat memory layout.
    *   Standard Go slice indexing (when BCE hints are present) is already highly optimized; `unsafe.Add` adds complexity without helping the branch predictor.

---

## Attempt 4: Manual Loop Inlining
*   **Changes:**
    *   Manual inlining of `Decode` and `DecodeBit` logic directly into tree and literal decoding cycles within `processNextOp`.
    *   Replaced high-level tree traversal loops with flat, unrolled or tight bit-processing cycles.
*   **Result:** **31.37 MB/s**
*   **Conclusion:** **SUCCESS.** Removing the overhead of virtual dispatch and function calls per bit is the most effective optimization for Go.

---

## Attempt 5: Flat Array DOD (Data-Oriented Design)
*   **Changes:**
    *   Eliminated slice headers (`[]prob`) in codecs, replacing them with static embedded arrays (e.g., `[64]prob`).
    *   Used pointer access (`probs := &codec.probs`) to prevent array copying on stack.
    *   Streamlined `deepcopy` to use native structure assignment (optimized `memmove`).
*   **Result:** **31.20 MB/s (NO CHANGE)**
*   **Conclusion:** Go handles small slice headers very efficiently. While the code is cleaner and avoids heap allocations, it doesn't bypass the serial dependency of the Range Decoder.

---

## Attempt 6: Slice BCE Trick (Bounds Check Elimination)
*   **Changes:**
    *   Attempted to eliminate internal array bounds checks by using locally scoped slices (`buf[:limit]`) and `if pos < len(buffer)` hints.
*   **Result:** **29.34 MB/s (REGRESSION)**
*   **Conclusion:** **FAILED.** Extra variables for slice headers increased "Register Pressure". The Go compiler was forced to "spill" hot variables to memory, killing performance.

---

## Attempt 7: Proactive Buffering (Blind Reading)
*   **Changes:**
    *   Introduced `ensureMargin(64)` to guarantee data availability before decoding a symbol.
    *   Removed all `if pos < limit` checks and `updateCodeSlow` calls from inner loops to allow "blind" reading.
*   **Result:** **32.00 MB/s (NEGLIGIBLE GAIN)**
*   **Conclusion:** Modern CPU Branch Predictors are so efficient that simple safety checks (`if pos < limit`) are nearly free. The serial data dependency of the Range Decoder is the absolute bottleneck.

## Attempt 8: Allocation & Struct Optimization (The Garbage Collector Wall)
*   **Changes:**
    *   Replaced the heavily boxed `operation` interface inside encoder loops with a compact, 16-byte value-struct (`distance int64`, `length int32`, `lit byte`, `typ opType`).
    *   Optimized `literalCodec.init` and `literalCodec.deepcopy` to reuse slice capacity instead of always allocating on the heap.
    *   Avoided fresh allocations of `state` during LZMA2 property resets by reusing and resetting the existing instance.
    *   Introduced `sync.Pool` for concurrent output buffers (`bytes.Buffer`) in parallel encoding.
*   **Result:** **~34 MB/s (Solid Unpack) / ~92 MB/s (Files Unpack)**. Allocations dropped by **98%** (from 2.1M to 45K allocs/op on Solid Pack).
*   **Conclusion:** **SUCCESS.** Drastically reduced heap allocations and GC overhead, making the engine much cleaner and more stable.

## Attempt 9: Branchless Range Decoding (Breaking the Branch Predictor)
*   **Changes:**
    *   Refactored the range decoder's hot-path loops (`prob.go`, `decoder.go`, `treecodecs.go`, `distcodec.go`, `literalcodec.go`) to use branchless bitwise masking instead of conditional jumps.
    *   Constructed a sign-extended mask using `uint32((int64(code) - int64(bound)) >> 63)` to completely bypass unpredictable branches (`if code < bound`).
*   **Result:** **~40 MB/s (Solid Unpack) / ~104 MB/s (Files Unpack)**.
*   **Conclusion:** **SUCCESS.** Eliminating branch misprediction penalties in the serial bottleneck of range decoding allowed the instruction pipeline to remain saturated.

## Attempt 10: Flat Array Data-Oriented Design (DOD)
*   **Changes:**
    *   Flattened nested codec slice structures (`probs []prob` inside `treeCodec` and `literalCodec`) into contiguous flat arrays within `lengthCodec` and `distCodec`.
    *   Rewrote `state.deepcopy()` as a single block assignment (`*s = *src`) to copy all properties using optimized `memmove`.
*   **Result:** **~39 MB/s (Solid Unpack) / ~101 MB/s (Files Unpack) (REGRESSION)**
*   **Status:** **FAILED / ROLLED BACK.**
*   **Post-Mortem:** Reusing arrays ballooned the `state` struct size to ~28 KB. Copying 28 KB on every chunk/block boundary during parallel compression congested L1 write ports. Furthermore, indexing multidimensional arrays increased "Register Pressure" on Go's SSA backend.

## Attempt 11: Zero-Overhead Dictionary Access
*   **Changes:**
    *   Added a specialized `lastByte()` method in `decoderDict` to fetch the last decoded byte without distance checks.
    *   Optimized `byteAt` by replacing `dictLen()` and capacity method calls with a simple `int64(dist) > d.head` boundary check.
*   **Result:** **No noticeable change (NO CHANGE)**
*   **Status:** **FAILED / ROLLED BACK.**
*   **Post-Mortem:** Modern CPU Out-of-Order Execution engines are so efficient that simple metadata calculations are executed in parallel with the Range Decoder bottleneck, resulting in no measurable speedup.

---

## Final Insights: The "Go Compiler Wall"
1.  **Register Pressure:** Go's SSA backend has a limited number of registers. Adding even a single extra variable or slice header to the hot loop can trigger stack spilling, which is costlier than a bounds check.
2.  **Computational Limit:** Single-threaded LZMA serial decoding in Go has a practical limit of ~40 MB/s on this hardware.
3.  **Branch Prediction:** Don't obsess over simple branches; modern CPUs handle them perfectly if they are predictable. However, unpredictable branches in range decoder hot loops should be aggressively eliminated using bitwise masks.
4.  **The Path to 100+ MB/s:** To match C++ performance, we must look beyond serial optimizations and implement **Parallel Block Decompression**, leveraging multiple CPU cores (already accomplished via `ParallelReader`).

## Summary of Lessons
*   **Inlining is king:** Manual inlining provided the biggest leaps (Attempt 2 & 4).
*   **Locality of variables > Locality of data:** Local variables (registers) are faster than optimized memory layouts in Go.
*   **Don't fight the compiler:** Complex "fast paths" often confuse the Go SSA optimizer.
---

## The Next Frontier: Parallel Block Decompression (The Holy Grail)

Our journey has taken us from **14 MB/s** to **~32 MB/s**, a 2.2x increase. However, the profiling data and the "Computational Limit" insight (Attempt 7) show that we have exhausted the possibilities of serial optimization in pure Go.

To reach the target of **80+ MB/s** (matching the 7z.so C++ implementation), we must pivot to a new architecture:

1.  **Block-Level Parallelism:** The XZ format is designed with independent blocks. Each block can be decompressed in its own goroutine.
2.  **Worker Pool:** Implement a pre-allocated pool of worker goroutines and a coordination layer (likely using `io.ReaderAt` or a smart chunking buffer) to feed blocks to these workers.
3.  **Linear Scaling:** Since LZMA decompression is CPU-bound and has minimal shared state between blocks, we expect near-linear scaling with the number of CPU cores.

**Objective:** Transform `unxed/xz` from a single-threaded sequential reader into a modern, multi-core processing engine.
