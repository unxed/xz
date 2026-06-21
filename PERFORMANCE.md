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

## Key Lessons
1.  **Don't fight the compiler with `unsafe`** unless you are doing SIMD/Vectorization. Standard slices are fast.
2.  **Locality of variables matters more than locality of data.** Keeping state in local variables (registers) gave a 35% boost, while flattening the probability data (Attempt 3) actually slowed it down.
3.  **Inlining is king.** The Go compiler is conservative with inlining. Manual inlining of bit-decoding logic is necessary for LZMA.
